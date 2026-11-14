# qrvt security

This document states what qrvt defends against, how, how that is tested, and how the final security
review before release is run. The design is in [DESIGN.md](DESIGN.md) and the wire format in
[FORMAT.md](FORMAT.md).

## 1. Position

qrvt reads video files and turns what it finds in them into files on disk. **Everything that comes out
of a video is hostile until proven otherwise.** That covers the recording file itself, the header and
descriptor read from its frames, the recovered stream, the archive inside it, and every file name.

Two guarantees are the goal:

1. A crafted video or recording cannot make `qrvt decode` crash the machine, run code, exhaust memory or
   disk, hang, or write outside the chosen output folder. For video decoding this guarantee rests on the
   sandbox alone (T1). It does not hold when the user leaves the sandbox with `--ffmpeg system` or the
   HEVC fast-path flag `--unsandboxed-hevc`; those are for recordings the user made themselves.
2. When the receiver names the senders they trust, a transfer that was not produced by one of them, or
   that was altered, is rejected before any of its content is decrypted or unpacked.

## 2. What is in scope

| In scope | Out of scope, by decision |
| --- | --- |
| Hostile recordings and videos given to `decode`, `inspect` and `calibrate read` | Marking decoded files with quarantine or Mark-of-the-Web flags |
| Hostile content reached through a genuine recording of a hostile screen | Scanning decoded files for malware |
| Forged or altered transfers | Hiding that a transfer is taking place |
| Confidentiality of the payload from someone else filming the screen, when a passphrase is used | Protecting a machine whose user account is already compromised |
| The integrity of released binaries | |

qrvt never opens, interprets or runs the files it decodes. Whether a decoded file is itself dangerous to
open is for the receiver and their operating system to judge.

## 3. Trust boundaries

```
recording file --> [ ffmpeg in WebAssembly sandbox ] --> raw frames
raw frames     --> [ vision, header, descriptor ]    --> soft bits, parameters
soft bits      --> [ LDPC, Reed-Solomon ]            --> stream
stream         --> [ hash, signature ]               --> body          <- authenticity is decided here
body           --> [ age, decompressor ]             --> archive
archive        --> [ manifest check, unpack ]        --> files in a staging folder --> output folder
```

Each arrow carries attacker-controlled data. Each box must hold when its input is arbitrary.

Only with the HEVC fast-path flag `--unsandboxed-hevc` (DESIGN.md section 12): the sandboxed engine copies an
HEVC stream out of its container and the pure-Go `gen2brain/h265`, outside the sandbox, turns it into
raw frames.

## 4. Threats and defences

| # | Threat | Defence |
| --- | --- | --- |
| T1 | A malformed container or bitstream exploits the video decoder | **The default path is fully sandboxed:** ffmpeg runs inside the wazero WebAssembly sandbox and parses every container and every bitstream, H.264 and HEVC alike. A decoding instance sees a read-only view of the single input file only (an `fs.FS` wrapper mounted with `FSConfig.WithFSMount`, which also counts reads for the watchdog; its files implement neither `io.Writer` nor `io.WriterAt`, so a guest write cannot reach the host file); no network; a memory ceiling; a watchdog (section 5) that cancels an instance making no progress for 30 s, with the runtime created with `WithCloseOnContextDone(true)` so that cancellation interrupts running Wasm; and a limit on decoded pixels per input byte (section 5). Its only output is raw frames on a pipe, which are length-checked; cancelling a decode closes that pipe, so an instance blocked writing a frame stops too. Every job that reads a recording passes `-protocol_whitelist file,pipe` and a `-format_whitelist` naming only the demuxers of the containers qrvt reads (MP4 and MOV, Matroska and WebM, MPEG-TS, H.264 and HEVC elementary streams), so ffmpeg refuses any other format, an HLS playlist among them, whatever the file's extension; a native ffmpeg is given the same options. A probe is bounded in reads and in time (section 5). The instance that copies an HEVC stream out of its container for the fast path sees the same read-only view of the input and one temporary directory, writable, nothing else; its output is a file in that directory, whose size is bounded (section 5, `video.FastPathCopySize`). A user leaves the sandbox in exactly two ways, both opt-in and both for recordings the user made themselves: `--ffmpeg system` (below) and the HEVC fast-path flag `--unsandboxed-hevc`, which decodes HEVC in the qrvt process with gen2brain/h265 v0.2.5 (created August 2026, no independent security review); the flag does not ship until that decoder can be built without its hand-written assembly, and until then the fast path is compiled only into builds with the `unsandboxedhevc` tag (other builds refuse the option). The fast path's memory is not bounded against a crafted stream (known weakness A, below) |
| T2 | A lying header or descriptor drives huge allocations or loops | Every field is used only after its CRC passes and its value is checked against section 5. Parameters derived from the stream length are recomputed and must match (FORMAT.md section 10). A decode locks onto the first valid session; frames from any other session are counted and ignored |
| T3 | Soft-bit or shard floods exhaust memory or disk | The soft-bit store is capped and evicts. The shard store is a file whose size is fixed by the validated descriptor, and free disk space is checked before it is created |
| T4 | A decompression bomb | Decompression is streaming and stops at the archive length declared in the stream header. That length must not exceed `--max-size` or the free space on disk |
| T5 | An archive writes outside the output folder or replaces something it should not | The whole manifest is validated against the path rules (FORMAT.md section 12) before any file is written. Only regular files and directories are accepted. Files are written through a directory-rooted handle (`os.Root`) into a new staging folder inside the output folder, their hashes are checked, and only then are they moved into place. Existing files are not overwritten without `--overwrite`. Permission bits are limited to the low nine |
| T6 | File names inject terminal escape sequences | Every string that originates in the input is stripped of control characters before it is printed, in the dashboard, plain and JSON renderers alike |
| T7 | A forged or altered transfer | An optional Ed25519 signature over the stream's header and body, checked before decryption and unpacking (section 6) |
| T8 | Corruption passes as success | The stream's SHA-256 is checked before anything else is done with it, and each file's SHA-256 is checked before it leaves staging. The outcome is correct files or an error, never quietly wrong files |
| T9 | A tampered release or dependency | Section 8 |
| T10 | A passphrase or private key leaks through the command line | Passphrases are read from a prompt or a file, never from an argument. Private key files are created with owner-only permissions, and `qrvt` warns when it reads one that is more widely readable |

### What a native ffmpeg changes

`--ffmpeg system` runs an external ffmpeg with no sandbox, so T1 is then defended only by that ffmpeg's
own robustness. The help text and user guide say so, and recommend it only for recordings the user made
themselves.

### What the HEVC fast path changes

The HEVC fast-path flag `--unsandboxed-hevc` is in the same trust class as `--ffmpeg system`. The help text,
the user guide and DESIGN.md section 13 say three things: it is for recordings the user made themselves,
it runs outside the sandbox, and its memory is not bounded against a crafted stream. The protections of
DESIGN.md section 12 (built without its hand-written assembly; exactly `Threads(1)`; 4:2:0 at 8 or 10 bits; at most 7680 x 4320; a buffer of at
most 6 pictures; a recover boundary per slice; fallback to the engine on error) reduce the risk. They do
not establish a bound. The copy instance's temporary directory may hold at most twice the input's size
plus 64 MiB (`video.FastPathCopySize`), checked once a second; beyond that the copy is cancelled and the
decode stops with an error naming the limit. The flag does not ship until the decoder can be built without
its hand-written assembly (DESIGN.md section 12).

**Known weakness A.** `gen2brain/h265` never checks a slice's reference picture set against the buffer
size its sequence parameter set declares (`sps_max_dec_pic_buffering_minus1`). For every reference the
set names that the buffer lacks, `generateUnavailable` (`hevc/dpb.go`) allocates a full-size grey
picture: up to 16 + 16 short-term and 32 long-term, about 6.5 GB at 7680 x 4320 or 1.6 GB at
3840 x 2160 (10 bits). A crafted stream can therefore exhaust memory on the fast path. For version 1
there is no patch, no vendored copy and no slice-header parser to prevent it; the fast path must never
be mistaken for safe.

## 5. Limits

Fixed by the format: grid at most 2,047 cells each way; at most 4,194,300 frames; at most 32,768 data and
32,768 parity shards per block.

Implementation defaults, each a named constant with a test that exceeds it:

| Limit | Default |
| --- | --- |
| Unpacked size (`--max-size`) | 16 GiB. No ratio limit is applied, because a legitimate sparse file compresses by more than any sensible ratio |
| Stream length | Free disk space less 1 GiB, and at most 64 GiB |
| Manifest (`--max-entries`) | 100,000 entries (`DefaultMaxEntries`) and 6,710,886 bytes (`DefaultMaxManifestBytes`, 64 MiB / 10). `--max-entries N` raises the entry limit to N and the size limit to N x 64 MiB / 1,000,000 bytes; the ceiling is the format limit, 1,000,000 entries and 64 MiB |
| Soft-bit store | 256 MB |
| LDPC iterations per codeword | 25 |
| Sessions tracked per decode | 1 |
| Engine progress (`EngineStallTimeout`) | 30 s. An engine instance (decoding, probing, or copying a stream for the HEVC fast path) that makes no progress for 30 s is cancelled, and the decode stops with an error naming the limit. Progress is a complete output frame with its timestamp when decoding (a frame trickled a byte at a time is not progress), input read through the counting `fs.FS` view when probing, growth of the output file when copying, and a frame taken from qrvt when encoding; time an instance spends waiting on qrvt (a full output pipe, an empty input pipe, a turn to run, the module's first compilation) is not a stall, except while qrvt is itself waiting on that instance for a frame's timestamp, a wait that also ends after 30 s. Ticks once a second, so a stall is stopped 30-31 s after the last progress. At most GOMAXPROCS - 1 instances run at once (`video.EngineInstances()`, at least one). The cap was first justified by saying that compiled Wasm cannot be preempted; that is not so with `WithCloseOnContextDone(true)`, under which every loop back-edge of the compiled code returns to Go for its check, where the scheduler can preempt it. Without the cap the watchdog still stops every instance, but uncapped, stops were up to 8 times late on a CI runner: on macos-26-intel, GOMAXPROCS + 1 spinning instances took 16.8 s against a 2 s limit, while with the cap they stayed within its bound (on a 16-thread development machine GOMAXPROCS + 2 uncapped instances were stopped on time). The cap therefore stays as a real defence (`TestWatchdogUnderLoad` asserts the capped case and logs the uncapped one). Waiting for a turn has no limit but the command's context, so a caller that keeps that many decoders open without reading them waits for ever (`TestHoldAndWait`); qrvt's workers never exceed the cap (DESIGN.md section 8). The margin: with close-on-cancel on, the engine decodes 4K HEVC at about 0.28 frames per second (about 3.6 s per frame, spike S1 section 10), so 30 s is about 8 times a 4K frame and, at four times the pixels, about 2 times an 8K frame; without close-on-cancel (about 0.3 s per 4K frame) it would be about 100 times. The worker processes of PR #15 run the engine in a child process with close-on-cancel off, removing it from the default path. This bounds stalls only, not total decode time; `MaxDecodedPixelsPerInputByte` bounds the work a file can demand. Implemented: `video.EngineStallTimeout` |
| Probe (`EngineProbeTime`, `MaxProbeReads`) | 15 s of running time, not counting time waiting for a turn or for the module's first compilation, and at most 4 times the file's size plus 16 MiB read through the counting view, both checked once a second; beyond either the probe is cancelled with an error naming the limit. Measured with close-on-cancel on: probing the test clips, the four S1 pattern files and the six S2 iPhone recordings (up to 2.6 GB; H.264 and HEVC up to 4K; MP4, MOV, Matroska and MPEG-TS) took at most 1.2 s and read at most 1.07 times the file's size (MPEG-TS; the large MP4 and MOV files read under 1%), so 15 s is about 12 times the slowest. The format whitelist (T1) refuses at once the 110-byte HLS playlist that held ffprobe for more than 8 minutes; these bounds cover what the whitelist lets through. A native ffmpeg's reads are not counted; its probe has the time bound only. Implemented: `video.EngineProbeTime`, `video.MaxProbeReads` |
| Decoded pixels per input byte (`MaxDecodedPixelsPerInputByte`) | 10,000. Checked first from the probe (frame count x frame size against the file size), so a hostile file is refused before decoding starts, and enforced again while decoding (pixels decoded so far against the file size), in case the probe was lied to. Exceeding it stops the decode with an error naming the limit. Measured in spike S1: 27.5 (1080p30 H.264 clip at 18 Mbit/s), 75.4 and 75.5 (4K60 HEVC clips at 53 Mbit/s), and 16.6-20.6 for the four S2 pattern files; a real 4K60 HEVC recording at 53 Mbit/s is about 75 and 1080p30 at 8 Mbit/s about 60, so the limit leaves more than 100 times headroom; the six S2 iPhone recordings measure 12-41 (S1 section 12). That headroom applies to recordings that mostly show the code: a recording that is mostly something else (a long static scene, the phone lying face down) compresses far better and can exceed the limit; the error gives the measured ratio and the limit and says that trimming the recording to the part that shows the code fixes a legitimate file. No flag raises it. The frame count comes from the container's frame count, else from its duration times the frame rate; a file that states neither is checked only while decoding. The bound is proportional to file size: at the limit one megabyte may demand 10^10 decoded pixels, about 1,200 4K frames, or about 5 instance-minutes at the engine's 4K HEVC speed. Decoding passes `-fps_mode passthrough`, so ffmpeg never duplicates frames to fill gaps in a file's timestamps. Implemented: `video.MaxDecodedPixelsPerInputByte`; the skip-picture file of section 9.5 measures 139,000 |
| Engine memory per instance | 512 MiB of linear memory, decoding and encoding, reserved up front (the reservation is the ceiling). Spike S1 measured at most 252 MB in use; process memory reached 920 MB per instance when the whole reservation became resident, and a 1 GiB reservation raised that to 1,434 MB. Beside it, a decoder reads frames straight into the caller's planes through a 256 KiB buffer; at the capture limit (7,680 x 4,320, 4:2:0, 16-bit samples for 8 and 10 bits alike) the caller's frame is 99.5 MB, so a decoder at that limit holds about 640 MB with the reservation, and about 1,020 MB with the worst-case 920 MB above, which fits section 9.5's 1 GiB (1,074 MB) with about 54 MB to spare. Until the review of PR #9 the capture limit was 8,192 x 8,192 and the decoder also kept a frame-sized pipe buffer: 402 MB in 10 bits, about 1.3 GB in the worst case |
| Capture size accepted from the engine or the HEVC decoder | 8K UHD in either orientation: the longer side at most 7,680 and the shorter side at most 4,320 pixels (`video.MaxCaptureLongSide`, `video.MaxCaptureShortSide`; some cameras code portrait recordings as 4,320 x 7,680 instead of tagging a rotation). It matches the fast path's limit and is above the largest phone recording. Checked from the probe before anything is allocated, and by the fast path's parameter-set check; every decoded frame must then have the probed size, and a change of size mid-stream stops the decode (ffmpeg would otherwise scale it silently, hiding the decoding work); an odd width or height is refused from the probe (`video.ErrUnsupported`), since the 4:2:0 chroma planes of such a picture would be mis-framed |
| HEVC fast path (opt-in only) | Exactly `Threads(1)`. Before decoding, every sequence parameter set is checked, and the slice goes to the engine unless it is 4:2:0 at 8 or 10 bits, at most 7680 x 4320 luma samples (also set as `FrameSizeLimit`), with `sps_max_dec_pic_buffering_minus1[i] + 1 <= 6` for every sub-layer i, and every one declares the probed size, chroma format and bit depth (a stream that changes them goes to the engine). These reduce risk; they do not bound memory (known weakness A). No byte bound is claimed |
| HEVC fast path (opt-in only): copy size | Twice the input file's size plus 64 MiB for the copy instance's temporary directory (`video.FastPathCopySize`), checked once a second; beyond it the copy is cancelled and the decode stops with an error naming the limit. A legitimate copy is smaller than the input, plus about 70 bytes of timestamps per picture |
| HEVC fast path (opt-in only): decode time | 10 s per picture, counted from its first slice (`video.FastPathPictureTime`). The decoder cannot be cancelled, so at the first picture that exceeds the limit the whole decode stops with an error naming the limit; no abandoned decoder is left running while work continues. Does not apply to the default path |

A decode that reaches a limit stops with an error that names the limit and, where one exists, the flag
that raises it. Two limits have a flag in version 1: the unpacked size (`--max-size`) and the manifest
(`--max-entries`, up to 1,000,000 entries and 64 MiB).

## 6. Sender authenticity

- `qrvt keygen -o name` writes an Ed25519 key pair: `name.key` (owner-readable only) and `name.pub`.
  Keys are one line of text: `qrvt-private-key-v1:<base64 of the 32-byte seed>` and
  `qrvt-public-key-v1:<base64 of the 32-byte key>`. A key's fingerprint is the first 16 hexadecimal
  digits of its public key's SHA-256.
- `encode --sign-key name.key` appends the public key and a signature to the stream (FORMAT.md section 12).
- `decode` behaves as follows:

| Transfer | `--verify-key` | Result |
| --- | --- | --- |
| Unsigned | not given | Proceeds; the summary says "unsigned" |
| Signed, signature valid | not given | Proceeds; the summary shows the fingerprint and says it was not checked against a trusted key |
| Signed, signature invalid | given or not | Fails before decryption and unpacking |
| Signed, signature valid, by one of the keys given | given | Proceeds; the summary says "verified" |
| Unsigned, or signed by any other key | given | Fails before decryption and unpacking |

Giving `--verify-key` is what turns enforcement on. There is deliberately no mode that checks a signature
when one is present but accepts its absence: an attacker would simply leave the signature out.

What a verified signature proves: the holder of that private key produced exactly this stream. What it
does not prove: that the video is recent. A recording of an old signed video verifies again.

The signature is over ciphertext when the transfer is encrypted, so it can be checked without the
passphrase. The signer's public key is visible to anyone who films the screen.

## 7. Confidentiality

With `--encrypt`, the body is encrypted in the age format under a key derived from the passphrase with
scrypt. Someone else filming the screen obtains only ciphertext, and its secrecy is as strong as the
passphrase. age authenticates its ciphertext, so tampering is also detected at decryption. Without
`--encrypt`, anyone who films the screen can decode the files.

## 8. Supply chain

- Dependencies are pinned in `go.mod` with checksums in `go.sum`; the embedded ffmpeg module is pinned
  by version and its WebAssembly binary by SHA-256, checked in a test.
- Every pull request runs `govulncheck`, CodeQL and a licence-compatibility check.
- CI actions are pinned by commit hash. Workflows declare the least token permissions they need, never
  run on `pull_request_target`, and never expose secrets to pull requests from forks.
- Releases carry checksums, a keyless cosign signature, an SBOM and build provenance; macOS binaries are
  notarised and Windows binaries signed when the certificates are configured.
- The repository contains the script that rebuilds the embedded ffmpeg from source, and the licence
  notices its GPL terms require.

## 9. Security test plan

### 9.1 Fuzz targets

Each runs briefly on every pull request and for longer nightly, with its corpus committed.

| Target | Must hold |
| --- | --- |
| Header decoding from arbitrary soft bits | No panic; a result only with a valid CRC |
| Descriptor parsing and plan derivation | No panic; no allocation before validation |
| Viterbi and LDPC decoding from arbitrary soft bits | No panic; terminates within the iteration cap |
| Stream header, manifest and key-file parsing | No panic; rejects everything FORMAT.md forbids |
| Unpacking arbitrary archive bytes into a temporary root | No file created outside the root; no panic |
| Finder detection and mesh fitting on arbitrary images | No panic; time bounded by image size |
| HEVC fast path only (`FuzzFastPathNAL` in `internal/video`, built with the `unsandboxedhevc` tag): HEVC decoding (`gen2brain/h265`, pure Go, `Threads(1)`) of arbitrary NAL units, seeded with the test clips and the HEVC conformance streams | No panic escapes the recover boundary; every stream refused by the parameter-set check of section 5 is handed to the engine; no picture larger than 7680 x 4320 is allocated. The fuzzer reports inputs that take longer than the per-picture limit or allocate more than the worker budget; it bounds neither. The fuzz task (`tools/dev fuzz`) adds the tag, so the CI and nightly runs fuzz it |
| Whole decode from arbitrary raw frames, seeded with valid and near-valid frames | No panic; reports success only when the recovered stream matches the descriptor's hash; writes nothing outside the root |

### 9.2 Hostile corpus

Fixtures under `testdata/hostile/`, each with the exact error it must produce, run on Linux, macOS and
Windows:

- **Paths**: absolute; `..`; backslashes; drive letters and colons; NUL and control characters; ANSI
  escape sequences; trailing dot or space; every Windows device name with and without an extension;
  over-long components and paths; two names differing only in case or normalisation; a file under a file.
- **Archive entries**: symbolic links, hard links, devices, FIFOs; entries not in the manifest; entries
  out of order; sizes or hashes that disagree with the manifest.
- **Sizes**: an archive length far beyond `--max-size`; a body that decompresses past its declared
  length; a manifest at and beyond its limits; a stream length beyond free disk space.
- **Format**: every reserved field non-zero; unknown versions and ids; a descriptor that disagrees with
  the header or with the values derived from its own stream length; frames from two sessions interleaved.
- **Video files**: truncated, zero-length, wrong container, and structurally corrupt files, and a live
  HLS playlist named `.mp4`. Each must end in a clear error within a time bound; the playlist within 5 s.

### 9.3 Sandbox tests

- The embedded engine, asked to read a path outside its view, fails.
- The embedded engine, asked to write during a decode, fails.
- An engine instance that exceeds its memory ceiling is stopped and reported, and the process survives.
- Cancelling the command stops every engine instance promptly.
- A Wasm instance that spins without producing output is stopped by the watchdog of section 5 within
  30 s plus a margin of 5 s; the process survives and reports the limit by name. Written in M1
  stream D, with the watchdog.
- A crafted HEVC stream whose slice lists 64 missing references, decoded by the sandboxed engine (the
  default path), stays within the engine's 512 MiB ceiling and fails cleanly. Written in M1 stream D.
- With the HEVC fast-path flag: the pure-Go decoder and the engine give identical frames on the
  test clips; the wrapper accepts exactly `Threads(1)` and refuses every other value, including 0
  (which the library takes as GOMAXPROCS) and 2; a panic raised inside the decoder's call path (from
  within `DecodeNAL`, through a test decoder, not only in the caller's own code) is recovered and the
  slice is decoded by the engine; a decode that exceeds the time limit stops the whole decode with an
  error naming the limit.

Implemented in `internal/video` (`sandbox_test.go`; the fast-path tests in `fastpath_test.go` need the
`unsandboxedhevc` tag). The read test asks for absolute, relative, `..` and `file:` paths outside the
view; the write test runs through the counting `fs.FS` view itself, including an attempt to overwrite
the input, and checks that the input is unchanged and nothing appears beside it. The memory test asks
for a 968 MB picture, which the engine reports as "Out of memory". The cancel test cancels a spinning
instance, a decoder and an encoder at once; all stop within 3 s. The missing-references test crafts a
3840 x 2160 10-bit stream (a buffer of 16 pictures): with 16 + 16 short-term and 32 long-term
references the engine refuses the slices ("Too many refs in a short term RPS"); with 15 + 15 + 2 it
allocates missing pictures until its linear memory reaches 508 MiB, then reports "DPB full" and
finishes; the process survives both. It takes 15-20 s, because close-on-cancel makes the engine's
picture-filling loops slow (DESIGN.md section 12).

The decoder's own stalls are tested with a fake engine (`decoder_test.go`): frames that arrive without a
timestamp line, and a frame trickled a byte every 500 ms, are each stopped at a shortened stall limit with
an error naming it; cancelling stops, within 2 s, a decoder waiting for a timestamp and an instance
blocked writing a frame nobody reads; more timestamp lines than frames is refused, at the end of the file
and at the end of a slice. Anchoring the showinfo match at the start of a line keeps out container
metadata, which ffmpeg prints indented, but not all text from the file: some is logged from the start of
a line, such as an unknown Matroska CodecID, which may hold a newline. Frame lines must therefore also
count 0, 1, 2 ... after each configuration line, and a line out of sequence fails the decode.
`TestMetadataInjection` gives a clip container metadata shaped like a showinfo line and requires
unchanged timestamps; `TestCodecIDInjection` (the reviewer's case) gives a Matroska file's second track
a CodecID holding a newline and a showinfo line, and requires that no frame comes back mis-timed (the
decode fails). `TestCaptureLimits` has the engine write a portrait-coded 4,320 x 7,680 clip, accepted,
and clips one even step over on the longer and the shorter side, refused from the probe (the probe's
unit test and the fast path's parameter-set test cover one sample over), and a stream whose picture
size changes, which stops when it does. With the fast-path tag (a CI step runs these
tests on all six runners): the copy instance is stopped at its size bound, the engine's takeover abandons
the old decoder without flushing it, and two 0.6 s slices of one picture exceed a 1 s picture limit
together.

### 9.4 Authenticity tests

For signed transfers: flipping any single bit of the stream header, body, public key or signature is
rejected; nothing is written to the output folder; with `--verify-key`, unsigned transfers and transfers
signed by any other key are rejected; verification happens before the passphrase is requested.

### 9.5 Resource tests

Decoding each hostile input that aims at a limit in section 5 stays under 1 GiB of peak memory and
finishes within 60 seconds on a CI runner. Both are asserted in the test. Among them: a small file of
many skip pictures (a few kilobytes declaring hours of 4K video) is refused from the probe by
`MaxDecodedPixelsPerInputByte`, and the same file with a lying container is stopped while decoding.
Written in M1 stream D: `testdata/video/skip.mkv` holds 600 flat 3840 x 2160 pictures (20 s) in 36 KB
and is refused from the probe at about 139,000 pixels per byte; with its Matroska duration
rewritten to 0.1 s the probe promises 3 frames and decoding stops after 43 frames, with the engine's
linear memory at 76 MiB and the process's Go heap 245 MiB above where it started. The video-file cases of section 9.2 are tested there too (`TestHostileFiles`):
empty, truncated, text and PNG content, an MP4 without its track or without its codec configuration,
random NAL units and a live HLS playlist named `.mp4` each end in `video.ErrInvalidVideo`, the playlist in
under 5 s (the format whitelist refuses it; `TestProbeBound` shows that without the whitelist the probe's
time bound stops it). The 1 GiB holds for these inputs. For a file at the capture limit it holds by
construction, not by test: a decoder holds about 640 MB, and about 1,020 MB with the whole reservation
resident, about 54 MB under 1 GiB (section 5).

### 9.6 Static checks

The race detector on every pull request, CodeQL, `gosec` rules through the linter, and a test over the
root package, `cmd/` and `internal/` that fails if any package other than `internal/video` imports
`os/exec`, or if any package imports `unsafe`. Developer tooling under `tools/` and throwaway code under
`spikes/` are outside that rule, since they must launch other programs.

## 10. Final security pass (milestone M8)

The pass happens when the tool is feature-complete and before the first release candidate is tagged.

1. **Fresh reviewers.** Each reviewer is an agent that has not seen the implementation history. It is
   given this document, FORMAT.md and the code, and one attack surface:
   - recording file to raw frames (engine wrapper and sandbox configuration);
   - raw frames to soft bits (vision, header, descriptor);
   - soft bits to stream (LDPC, Reed-Solomon, stores);
   - stream to files (hash, signature, age, decompression, manifest, unpacking);
   - command line, key and passphrase handling, output sanitising;
   - CI and release workflows, dependency pinning, the embedded binary's provenance.
2. **Automated review** of the complete diff against the empty repository.
3. **Each reviewer attempts to break guarantees 1 and 2** in section 1 for its surface and writes a
   failing test for anything it finds.
4. **Findings** are recorded in `docs/security/REVIEW.md` with severity, the failing test, the fix, and
   the reviewer's confirmation that the fix holds.
5. **Exit.** No open finding of high or medium severity; every section 9 test passes on all three
   systems; the limits table matches the constants in the code.

Severity: *high* breaks a guarantee in section 1; *medium* weakens a defence in section 4 without yet
breaking a guarantee, or is a denial of service that stays within the limits; *low* is everything else.

## 11. Reporting a vulnerability

Report privately through the repository's "Report a vulnerability" page (GitHub private vulnerability
reporting), not in a public issue. The root `SECURITY.md` created in milestone M7 repeats this
instruction and states which versions receive fixes.

## Change log

| Date | Change |
| --- | --- |
| 2026-11-13 | M0 spikes: HEVC is decoded by the pure-Go `gen2brain/h265` outside the sandbox (spike S1). Section 3 shows the new boundary; T1 states what is and is not sandboxed; section 5 adds the HEVC decoder's limits; section 9.1 adds its fuzz target and section 9.3 its comparison and fallback tests |
| 2026-11-13 | M0 review: T1 states that the HEVC decoder runs with `Threads(1)` inside per-slice recover boundaries, that excluding its assembly is a requirement whose mechanism is decided in M1, and its version, age and lack of review. Section 5: the engine's ceiling is 512 MiB for decoding and encoding, reserved up front (a 1 GiB reservation raised worst-case memory in spike S1); the HEVC decoder's parameter-set checks and worst-case memory; the time limit abandons rather than stops a decode, with a limit on abandoned decodes. Sections 9.1 and 9.3 claim only what can be tested |
| 2026-11-13 | M0 review round 2 and the owner's decision for version 1: every codec, HEVC included, is decoded in the sandbox, and guarantee 1 rests on the sandbox alone (section 1). T1 names the two ways to leave the sandbox, `--ffmpeg system` and the HEVC fast-path flag (named in M1); a new subsection gives the fast path's trust class and known weakness A (missing references allocate full pictures, about 6.5 GB at 8K), withdrawing the 720 MB figure. Section 5 labels the fast path's limits as reducing risk, not bounding it, and its time limit stops the whole decode at the first abandon. Section 9.1 claims no time or memory bound; section 9.3 requires exactly `Threads(1)` (testing 0 and 2) and adds the 64-missing-references test against the engine |
| 2026-11-13 | M0 review round 3: guarantee 1's "hang" is made true on the default path. Section 5 adds `EngineStallTimeout`, 30 s without progress (about 100 times the time per 4K HEVC frame), for decoding, probing and the stream-copy instance; T1 replaces "cancelled with the command's context" with that watchdog and close-on-cancel, and gives the copy instance's mounts; section 9.3 adds the watchdog test (M1 stream D). The fast-path subsection adds "built without its hand-written assembly" and that the flag does not ship until then |
| 2026-11-13 | M0 follow-up: section 5 states that `EngineStallTimeout` bounds stalls only, names how progress is measured (the counting `fs.FS` view when probing, output-file growth when copying), and adds `MaxDecodedPixelsPerInputByte` (10,000; checked from the probe and again while decoding; measured 16.6-75.5 on the S1 clips and pattern files); T1 gives the single-file view as an `fs.FS` mount, attaches the raw-frame output to the decoding instance and the output file to the copy instance, and rewords the fast-path sentence; section 9.5 adds the skip-picture test (M1 stream D) |
| 2026-11-13 | M1 setup: the HEVC fast-path flag is named `--unsandboxed-hevc` |
| 2026-11-14 | M1 stream F, PR #11 round 3 (the owner's decision): section 5 gives the manifest default as 100,000 entries and 6,710,886 bytes (a tenth of the format limit), raised by `--max-entries` up to 1,000,000 entries and 64 MiB; two limits now have a flag, the unpacked size and the manifest |
| 2026-11-13 | M1 stream D: T1 adds that the input view's files have no write methods and that the fast path exists only in `unsandboxedhevc` builds. Section 5: `EngineStallTimeout` counts a frame taken when encoding, ignores time spent waiting on qrvt, and relies on at most GOMAXPROCS - 1 instances running at once; `MaxDecodedPixelsPerInputByte` gives where the frame count comes from, that the headroom applies to recordings mostly showing the code, the trimming advice, and the work bound (about 5 instance-minutes per MB at 4K); the capture size is checked from the probe and size changes stop a decode; the fast path refuses parameter sets that differ from the probe and times each picture from its first slice. Sections 9.1, 9.3 and 9.5 name the implemented tests and their results |
| 2026-11-14 | M1 stream D, review of PR #9: T1 adds the protocol and format whitelists on every job that reads a recording (an HLS playlist named `.mp4` had held ffprobe for more than 8 minutes), the probe's bounds, closing the output pipe on cancellation, and the copy instance's size bound; the fast-path subsection states that bound. Section 5: decoding progress is a complete, timestamped frame, and qrvt waiting on an instance for a timestamp does not count as the instance waiting on qrvt; the instance cap is described as a precaution (with close-on-cancel, compiled loops return to Go at every back-edge; the CI delay that prompted it was more likely load), with the hold-and-wait hazard; new rows for the probe (`EngineProbeTime` 15 s, `MaxProbeReads` 4 x size + 16 MiB, with the measurements) and the fast path's copy size (`FastPathCopySize`, 2 x input + 64 MiB); the engine memory row gives a decoder's host memory at the capture limit (201 MB of planes, the frame-sized pipe buffer removed); odd picture sizes are refused. Section 9.1: the fuzz task builds with the fast-path tag. Section 9.2 adds the HLS playlist; section 9.3 names the decoder-stall, metadata-injection, capture-limit and new fast-path tests; section 9.5 states where the 1 GiB is and is not shown |
| 2026-11-14 | M1 stream D, CI on PR #9: section 5 corrects the instance cap's description: without it the watchdog still stops every instance, but on the macos-26-intel runner GOMAXPROCS + 1 uncapped spinning instances took 16.8 s against a 2 s limit, so the cap is a mitigation, not only a precaution |
| 2026-11-14 | M1 stream D, round 2 of the review of PR #9: section 5 lowers the capture limit to 8K UHD in either orientation (longer side 7,680, shorter side 4,320), restates a decoder's host memory at it (99.5 MB of planes; about 1,020 MB in the worst case, inside section 9.5's 1 GiB), records the cap's evidence (uncapped, stops were up to 8 times late on a CI runner) and restates the stall margin with close-on-cancel (about 8 times a 4K frame, 2 times an 8K frame; PR #15 removes close-on-cancel from the default path). Section 9.3 corrects the injection wording (text logged from the start of a line, such as a Matroska CodecID, passes the anchored match; frame lines must count in sequence) and names `TestCodecIDInjection` and the orientation cases; section 9.5 states that the 1 GiB holds at the capture limit by construction |
