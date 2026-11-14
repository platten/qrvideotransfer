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
| T1 | A malformed container or bitstream exploits the video decoder | **The default path is fully sandboxed:** ffmpeg runs inside the wazero WebAssembly sandbox and parses every container and every bitstream, H.264 and HEVC alike. A decoding instance sees a read-only view of the single input file only (an `fs.FS` wrapper mounted with `FSConfig.WithFSMount`, which also counts reads for the watchdog); no network; a memory ceiling; a watchdog (section 5) that cancels an instance making no progress for 30 s, with the runtime created with `WithCloseOnContextDone(true)` so that cancellation interrupts running Wasm; and a limit on decoded pixels per input byte (section 5). Its only output is raw frames on a pipe, which are length-checked. The instance that copies an HEVC stream out of its container for the fast path sees the same read-only view of the input and one temporary directory, writable, nothing else; its output is a file in that directory. A user leaves the sandbox in exactly two ways, both opt-in and both for recordings the user made themselves: `--ffmpeg system` (below) and the HEVC fast-path flag `--unsandboxed-hevc`, which decodes HEVC in the qrvt process with gen2brain/h265 v0.2.5 (created August 2026, no independent security review); the flag does not ship until that decoder can be built without its hand-written assembly. The fast path's memory is not bounded against a crafted stream (known weakness A, below) |
| T2 | A lying header or descriptor drives huge allocations or loops | Every field is used only after its CRC passes and its value is checked against section 5. Parameters derived from the stream length are recomputed and must match (FORMAT.md section 10). A decode locks onto the first valid session; frames from any other session are counted and ignored |
| T3 | Soft-bit or shard floods exhaust memory or disk | The soft-bit store is capped and evicts. The shard store is a file whose size is fixed by the validated descriptor, and free disk space is checked before it is created |
| T4 | A decompression bomb | Decompression is streaming and stops at the archive length declared in the stream header. That length must not exceed `--max-size` or the free space on disk |
| T5 | An archive writes outside the output folder or replaces something it should not | The whole manifest is validated against the path rules (FORMAT.md section 12) before any file is written. Only regular files and directories are accepted. Files are written through a directory-rooted handle (`os.Root`) into a new staging folder inside the output folder, their hashes are checked, and only then are they moved into place. Existing files are not overwritten without `--overwrite`. Permission bits are limited to the low nine. Each file is placed with a hard link, which fails if anything has appeared at its path since the check, and a failed placement undoes everything placed so far. On file systems without hard links (FAT, exFAT, some network shares) qrvt instead checks that the path is free and then renames the file into it: something created at that path by another program in the moment between the check and the rename is replaced. It falls back only when the link fails because hard links are not supported (EPERM, EOPNOTSUPP or ENOTSUP on Linux and macOS; ERROR_INVALID_FUNCTION or ERROR_NOT_SUPPORTED on Windows); any other link error stops the extraction and undoes it. With `--overwrite`, a file is replaced only if it is a regular file and not another name for a file the same transfer has just written |
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
not establish a bound. The flag does not ship until the decoder can be built without its hand-written
assembly (DESIGN.md section 12).

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
| Path depth | 64 components, a path rule of FORMAT.md section 12: a decoder refuses the whole transfer, an encoder refuses to encode and names every such path. Two reasons. On macOS, and partly on Windows, the time spent on each path component grows with the depth of the folder (the measurements under "Extraction work"), so a deep manifest within the size limits could keep extraction busy for hours. And a tree far deeper than operating-system shells handle is a nuisance payload in itself |
| scrypt work factor accepted (`ScryptWorkFactor`) | 18, which is 256 MiB of scrypt memory, and the factor the encoder writes. A higher factor in a received stream is refused before scrypt runs |
| zstd window accepted (`MaxZstdWindow`) | 8 MiB, the window the encoder uses; a frame asking for more, including a single-segment frame with a larger content size, is refused before its buffers are allocated |
| xz dictionary accepted (`XZDictCap`) | 8 MiB, the dictionary the encoder uses; a block asking for more is refused. qrvt parses the xz container itself (stream header, block headers, LZMA2 chunk headers, block padding and check, index, footer); the library's LZMA2 decoder sees only one block's chunks, one at a time, and is created with the dictionary of our own parse of the block header. The library never parses an xz block header, because its xz reader treats its dictionary setting as a minimum. Each chunk must be consumed exactly, so a chunk cannot hide bytes that another parser would read as a header. A body holds exactly one block (FORMAT.md section 12), so there is one LZMA2 decoder and one dictionary per body; a second block header is refused before anything is allocated for it. The library allocates for each properties change and state reset (literal tables of up to 24 KiB) and for each uncompressed chunk it reads (a 32 KiB copy buffer), so within the block: only the first chunk resets the dictionary; only the first LZMA chunk sets properties; a state reset comes only directly after uncompressed data and at most once per 32 KiB of it; consecutive uncompressed chunks are handed over together, up to 64 KiB; and a new run of uncompressed chunks starts at most once per 32 KiB of uncompressed data before it. **Live memory** while decoding: one dictionary (at most 8 MiB), one chunk (at most 64 KiB), one set of LZMA tables (at most 24 KiB) and fixed buffers. **Total allocation** for one body follows from these rules: the dictionary once; copy buffers and literal tables of at most about 3 bytes per byte of uncompressed-chunk data, plus 32 KiB; one chunk buffer per chunk, no larger than the chunk; and the library's fixed cost per LZMA chunk, which no rule limits, measured at about 200 bytes, or 20 bytes per input byte for chunks of 10 bytes, the smallest that hold data (`TestXZManyLZMAChunks`). Measured in the tests: under 1 MiB for a body refused before its block's data; the dictionary plus under 1 MiB for the review streams, refused at their second block, state reset or uncompressed run whatever their length (20,000 and 100,000 blocks; 1,000 and 200,000 state resets; 50,000 alternating runs; before these rules they allocated up to 785 GB, 2,960 MiB and 1,584 MiB); 12 MiB, 8 MiB of it the dictionary, for 500,000 consecutive one-byte uncompressed chunks (2 MB) in 36 ms. The fuzz target checks every input it tries against one dictionary plus 2 MiB plus 64 bytes per input byte |
| Extraction work | Bounded by the manifest limits and the 64-component path rule. The calls extraction makes to the operating system, and the path components in them, are linear in the number of folders and in files times depth; a test counts them, at depths within the rule and, below the path rules, at depth 1000. Folders are created and checked through their parent folder's handle; each file's path is resolved a few times. **Worst case within the limits.** The manifest that maximises files times depth is a chain of 63 one-letter folders holding 237,274 empty files of 64 components; it fills the 64 MiB manifest limit, and the 1,000,000-entry limit does not bind. One extraction of it into a new folder, measured once on each CI runner (`BenchmarkExtractWorstCase`, run by the nightly workflow's manual job `extraction-worst-case`): ubuntu-24.04 65 s; ubuntu-24.04-arm 38 s; windows-2025 1,179 s (20 minutes); windows-11-arm 946 s; macos-26 382 s; macos-26-intel 600 s. For comparison, the widest manifest (427,750 empty files of one component, also 64 MiB) took 46 s, 28 s, 384 s, 427 s, 403 s and 548 s on the same runners. So a crafted transfer within the limits can keep extraction busy for up to about 20 minutes on the slowest system measured. **Why the depth rule** (measured beyond what a decoder now accepts): 50 files beneath a chain of folders, extracted and then extracted again over itself, at depths 500 and 2000, took ubuntu-24.04 0.21 s and 1.3 s; ubuntu-24.04-arm 0.25 s and 1.3 s; windows-2025 7.4 s and 72 s; windows-11-arm 5.6 s and 67 s; macos-26 3.9 s and 78 s; macos-26-intel 4.7 s and 122 s. On Linux the time grows about linearly with depth; on Windows and macOS it grows 10 to 26 times for 4 times the depth, because resolving each component costs more in a deeper folder, so a deep manifest within the size limits could have kept extraction busy for hours. Progress is reported in every phase: entries checked, file bytes written, entries placed, folders finished. The operation can be cancelled while it checks the output folder, writes file data and places files: a cancelled extraction removes the staging folder and reverses everything it has placed. The last phase, which sets the modes and times of the folders it created after every file is in place, costs one change per folder and is not cancelled; a cancellation during it is ignored and the extraction succeeds |
| Soft-bit store | 256 MB |
| LDPC iterations per codeword | 25 |
| Sessions tracked per decode | 1 |
| Engine progress (`EngineStallTimeout`) | 30 s. An engine instance (decoding, probing, or copying a stream for the HEVC fast path) that makes no progress for 30 s is cancelled, and the decode stops with an error naming the limit. Progress is an output frame when decoding, input read through the counting `fs.FS` view when probing, and growth of the output file when copying. 30 s is about 100 times the measured time per 4K HEVC frame (about 0.3 s, spike S1). This bounds stalls only, not total decode time; `MaxDecodedPixelsPerInputByte` bounds the work a file can demand. Implemented in M1 stream D |
| Decoded pixels per input byte (`MaxDecodedPixelsPerInputByte`) | 10,000. Checked first from the probe (frame count x frame size against the file size), so a hostile file is refused before decoding starts, and enforced again while decoding (pixels decoded so far against the file size), in case the probe was lied to. Exceeding it stops the decode with an error naming the limit. Measured in spike S1: 27.5 (1080p30 H.264 clip at 18 Mbit/s), 75.4 and 75.5 (4K60 HEVC clips at 53 Mbit/s), and 16.6-20.6 for the four S2 pattern files; a real 4K60 HEVC recording at 53 Mbit/s is about 75 and 1080p30 at 8 Mbit/s about 60, so the limit leaves more than 100 times headroom. Implemented in M1 stream D |
| Engine memory per instance | 512 MiB of linear memory, decoding and encoding, reserved up front (the reservation is the ceiling). Spike S1 measured at most 252 MB in use; process memory reached 920 MB per instance when the whole reservation became resident, and a 1 GiB reservation raised that to 1,434 MB |
| Capture size accepted from the engine or the HEVC decoder | 8,192 x 8,192 pixels |
| HEVC fast path (opt-in only) | Exactly `Threads(1)`. Before decoding, every sequence parameter set is checked, and the slice goes to the engine unless it is 4:2:0 at 8 or 10 bits, at most 7680 x 4320 luma samples (also set as `FrameSizeLimit`), with `sps_max_dec_pic_buffering_minus1[i] + 1 <= 6` for every sub-layer i; a change of picture size, bit depth or chroma format starts a new decoder. These reduce risk; they do not bound memory (known weakness A). No byte bound is claimed |
| HEVC fast path (opt-in only): decode time | 10 s per picture. The decoder cannot be cancelled, so at the first picture that exceeds the limit the whole decode stops with an error naming the limit; no abandoned decoder is left running while work continues. Does not apply to the default path |

A decode that reaches a limit stops with an error that names the limit and, where one exists, the flag
that raises it. Two limits have a flag in version 1: the unpacked size (`--max-size`) and the manifest
(`--max-entries`, up to 1,000,000 entries and 64 MiB).

## 6. Sender authenticity

- `qrvt keygen -o name` writes an Ed25519 key pair: `name.key` (owner-readable only) and `name.pub`.
  Keys are one line of text: `qrvt-private-key-v1:<base64 of the 32-byte seed>` and
  `qrvt-public-key-v1:<base64 of the 32-byte key>`. A key's fingerprint is the first 16 hexadecimal
  digits of its public key's SHA-256. The base64 is standard, padded and canonical; one trailing line
  ending (LF or CRLF) is accepted and nothing else; a key file over 4 KiB is refused. `keygen` never
  replaces an existing file. Reading a private key file whose permissions give any access to group or
  others (any of the bits 0o077) produces a warning; on Windows, where access is governed by ACLs, no
  check is made.
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
| HEVC fast path only: HEVC decoding (`gen2brain/h265`, pure Go, `Threads(1)`) of arbitrary NAL units, seeded with the test clips and the HEVC conformance streams | No panic escapes the recover boundary; every stream refused by the parameter-set check of section 5 is handed to the engine; no picture larger than 7680 x 4320 is allocated. The fuzzer reports inputs that take longer than the per-picture limit or allocate more than the worker budget; it bounds neither |
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
- **Video files**: truncated, zero-length, wrong container, and structurally corrupt files. Each must end
  in a clear error within a time bound.

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

### 9.4 Authenticity tests

For signed transfers: flipping any single bit of the stream header, body, public key or signature is
rejected; nothing is written to the output folder; with `--verify-key`, unsigned transfers and transfers
signed by any other key are rejected; verification happens before the passphrase is requested.

### 9.5 Resource tests

Decoding each hostile input that aims at a limit in section 5 stays under 1 GiB of peak memory and
finishes within 60 seconds on a CI runner. Both are asserted in the test. Among them: a small file of
many skip pictures (a few kilobytes declaring hours of 4K video) is refused from the probe by
`MaxDecodedPixelsPerInputByte`, and the same file with a lying container is stopped while decoding.
Written in M1 stream D.

**Exception: extracting a legal manifest.** Extraction of a manifest that is within the limits is
bounded by the manifest limits (section 5, "Path depth" and "Extraction work"), not by 60 seconds:
the worst case measured takes up to about 20 minutes on Windows. During it, progress is reported in
every phase and the extraction can be cancelled. The 60-second bound applies to everything before
extraction: the manifest within the limits that maximises files times depth is validated and checked
against the output folder within 1 GiB, and on Linux within 60 seconds (written in M1 stream A).
Whether to lower the manifest limits instead is an open decision for the owner.

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
| 2026-11-13 | M1 stream A: section 6 states the key-file details implemented: canonical padded standard base64, one trailing LF or CRLF, a 4 KiB size limit, `keygen` never replaces a file, and the readable-key warning (any of the bits 0o077; not checked on Windows) |
| 2026-11-13 | M1 stream A, review of PR 7: section 5 adds the decoder's limits on the scrypt work factor (18), the zstd window (8 MiB) and the xz dictionary (8 MiB). The xz limit is enforced by a guard that reads the xz block headers before the library does, because the library treats its dictionary setting as a minimum. All three are refused before anything is allocated for them |
| 2026-11-13 | M1 stream A, review of PR 7 round 2: T5 says each file is placed with a hard link and a failed placement is undone, and that on file systems without hard links (FAT, exFAT, some network shares) a check-then-rename leaves a short window in which something created at the path is replaced. Section 5 states the memory a refused xz body may allocate (under 4 MiB before any block header is accepted; the dictionary cap plus 1 MiB after a valid one). It adds an "Extraction work" row: work counted by a test and linear in the manifest, times measured on the six CI runners, the Linux worst case, the faster-than-linear growth on Windows and macOS, and cancellation |
| 2026-11-13 | M1 stream A, PR 7, path depth: section 5 adds a "Path depth" row (64 components, a FORMAT.md section 12 path rule, with its two reasons). The "Extraction work" row is rewritten for the limit: the worst manifest within the limits (237,274 empty files of 64 components, 64 MiB) measured once on each of the six CI runners (38 s to 1,179 s), the widest manifest for comparison, the earlier deep-chain figures kept as the reason for the rule and labelled as beyond what a decoder accepts, and progress in every phase. Section 9.5 says the worst-case manifest is validated and pre-checked within 1 GiB, and on Linux within 60 s |
| 2026-11-14 | M1 stream A, second independent review of PR 7: section 5's xz row says qrvt parses the xz container itself, hands the LZMA2 decoder one chunk at a time with the dictionary of its own header parse, requires each chunk to be consumed exactly, and gives the refusal memory as measured (under 1 MiB before the first block's data; at most the block's dictionary plus 1 MiB after). The "Extraction work" row says the finish phase is not cancelled. T5 lists the link errors that fall back to the check-then-rename; any other stops and undoes the extraction. Section 9.5 states the exception for extracting a legal manifest (bounded by the manifest limits, up to about 20 minutes on Windows as measured, with progress and cancellation) and that the 60-second bound applies to everything before extraction |
| 2026-11-14 | M1 stream A, third independent review of PR 7: section 5's xz row says a body holds exactly one block, so one LZMA2 decoder and one dictionary per body, with the reason (a new 8 MiB dictionary per block let 100,000 tiny blocks cost 78 s and 785 GB of allocation), the chunk rules, and states live memory and total allocation separately as measured (one dictionary, one chunk and fixed buffers live; allocation under 1 MiB, or the dictionary plus 1 MiB, independent of stream length; 12 MiB for 500,000 one-byte chunks), with the fuzz target's allocation bound |
| 2026-11-14 | M1 stream A, fourth independent review of PR 7: section 5's xz row adds the limits on LZMA state resets and on new uncompressed runs, with the library allocations they bound, and states only bounds that hold: live memory; total allocation as derived from the rules plus the measured fixed cost per LZMA chunk (about 200 bytes, which no rule limits); the measured figures for the review streams; and the fuzz target's check described as a check of the inputs it tries |
| 2026-11-14 | M1 stream F, PR #11 round 3 (the owner's decision): section 5 gives the manifest default as 100,000 entries and 6,710,886 bytes (a tenth of the format limit), raised by `--max-entries` up to 1,000,000 entries and 64 MiB; two limits now have a flag, the unpacked size and the manifest |
