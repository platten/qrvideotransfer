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
   sandbox (T1): the WebAssembly sandbox confines the decoder, and each decoding engine instance runs in
   a worker process that qrvt's watchdog kills, so a hang or memory blow-up stops at that process. The
   parent's kill is the control: while a guest spins, the worker's own timers cannot be trusted
   (close-on-cancel is off there, and a spinning guest blocks Go's stop-the-world garbage collection),
   so the worker's exit when its pipe closes is best effort. It does not hold when the user leaves the sandbox with `--ffmpeg system` or the
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
| T1 | A malformed container or bitstream exploits the video decoder | **The default path is fully sandboxed:** ffmpeg runs inside the wazero WebAssembly sandbox and parses every container and every bitstream, H.264 and HEVC alike. A decoding instance sees a read-only view of the single input file only (an `fs.FS` wrapper mounted with `FSConfig.WithFSMount`, which also counts reads for the watchdog; its files implement neither `io.Writer` nor `io.WriterAt`, so a guest write cannot reach the host file); no network; a memory ceiling; a watchdog (section 5) that cancels an instance making no progress for 30 s, each decoding and probing instance running in its own worker process (DESIGN.md section 12), which the watchdog and cancellation kill: the parent's kill is the control. The worker's runtime has close-on-cancel off, so while a guest spins it never returns to Go, and the worker's own timers and goroutines cannot be trusted (a spinning guest also blocks Go's stop-the-world garbage collection in that process); its exit when qrvt's end of its pipe closes is best effort. The worker receives its job over a pipe and validates it, refusing a job of another protocol version (qrvt then runs the job in process, below), and qrvt bounds every reply before allocating and sanitises it before printing, cut on a character boundary. Until the worker reports the engine loaded, `EngineLoadLimit` (section 5) bounds it instead of the watchdog. **Encoding and joining runs (`Concat`) do not run in a worker and have no forced stop:** they run in qrvt's own process with close-on-cancel off, so neither the watchdog nor cancellation can interrupt their guest. That is acceptable because their input is qrvt's own frames and runs, not a file from outside. Cancelling them works by closing their input (`context.AfterFunc`), so they stop at their next read, within a frame. While their guest computes it does not return to Go, so it delays qrvt's own stop-the-world garbage collection (a 4K frame takes up to about 0.5 s); a guest that spun would stall the GC, and with it the whole qrvt process, the watchdog included. That matters to a host that encodes and decodes in one process: the decoding workers' watchdog runs in that process. When no worker can be started, the instance runs in qrvt's process with `WithCloseOnContextDone(true)`, so that cancellation interrupts running Wasm, at most GOMAXPROCS - 1 at once; and a limit on decoded pixels per input byte (section 5), enforced in the worker as a bound on its output as well as in qrvt. A worker is the same binary with the same privileges as qrvt: it bounds hangs and memory, it is not an operating-system sandbox; the WebAssembly sandbox confines the decoder. Its only output is raw frames on a pipe, which are length-checked; cancelling a decode closes that pipe, so an instance blocked writing a frame stops too. Every job that reads a recording passes `-protocol_whitelist file,pipe` and a `-format_whitelist` naming only the demuxers of the containers qrvt reads (MP4 and MOV, Matroska and WebM, MPEG-TS, H.264 and HEVC elementary streams), so ffmpeg refuses any other format, an HLS playlist among them, whatever the file's extension; a native ffmpeg is given the same options. A probe is bounded in reads and in time (section 5). The instance that copies an HEVC stream out of its container for the fast path sees the same read-only view of the input and one temporary directory, writable, nothing else; its output is a file in that directory, whose size is bounded (section 5, `video.FastPathCopySize`). A user leaves the sandbox in exactly two ways, both opt-in and both for recordings the user made themselves: `--ffmpeg system` (below) and the HEVC fast-path flag `--unsandboxed-hevc`, which decodes HEVC in the qrvt process with gen2brain/h265 v0.2.5 (created August 2026, no independent security review); the flag does not ship until that decoder can be built without its hand-written assembly, and until then the fast path is compiled only into builds with the `unsandboxedhevc` tag (other builds refuse the option). The fast path's memory is not bounded against a crafted stream (known weakness A, below) |
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
| Manifest (`--max-entries`) | 100,000 entries (`DefaultMaxEntries`) and 6,710,886 bytes (`DefaultMaxManifestBytes`, 64 MiB / 10). `--max-entries N` raises the entry limit to N and the size limit to N x 64 MiB / 1,000,000 bytes; the ceiling is the format limit, 1,000,000 entries and 64 MiB. The size limit usually binds before the entry limit: a manifest entry takes about 150 bytes plus its path, so about 40,000 typical entries fit at the defaults (the widest manifest measured at them, empty files with names of one to four characters, held 43,026). |
| Path depth | 64 components, a path rule of FORMAT.md section 12: a decoder refuses the whole transfer, an encoder refuses to encode and names every such path. Two reasons. On macOS, and partly on Windows, the time spent on each path component grows with the depth of the folder (the measurements under "Extraction work"), so a deep manifest within the size limits could keep extraction busy for hours. And a tree far deeper than operating-system shells handle is a nuisance payload in itself |
| scrypt work factor accepted (`ScryptWorkFactor`) | 18, which is 256 MiB of scrypt memory, and the factor the encoder writes. A higher factor in a received stream is refused before scrypt runs |
| zstd window accepted (`MaxZstdWindow`) | 8 MiB, the window the encoder uses; a frame asking for more, including a single-segment frame with a larger content size, is refused before its buffers are allocated |
| xz dictionary accepted (`XZDictCap`) | 8 MiB, the dictionary the encoder uses; a block asking for more is refused. qrvt parses the xz container itself (stream header, block headers, LZMA2 chunk headers, block padding and check, index, footer); the library's LZMA2 decoder sees only one block's chunks, one at a time, and is created with the dictionary of our own parse of the block header. The library never parses an xz block header, because its xz reader treats its dictionary setting as a minimum. Each chunk must be consumed exactly, so a chunk cannot hide bytes that another parser would read as a header. A body holds exactly one block (FORMAT.md section 12), so there is one LZMA2 decoder and one dictionary per body; a second block header is refused before anything is allocated for it. The library allocates for each properties change and state reset (literal tables of up to 24 KiB) and for each uncompressed chunk it reads (a 32 KiB copy buffer), so within the block: only the first chunk resets the dictionary; only the first LZMA chunk sets properties; a state reset comes only directly after uncompressed data and at most once per 32 KiB of it; consecutive uncompressed chunks are handed over together, up to 64 KiB; and a new run of uncompressed chunks starts at most once per 32 KiB of uncompressed data before it. **Live memory** while decoding: one dictionary (at most 8 MiB), one chunk (at most 64 KiB), one set of LZMA tables (at most 24 KiB) and fixed buffers. **Total allocation** for one body follows from these rules: the dictionary once; copy buffers and literal tables of at most about 3 bytes per byte of uncompressed-chunk data, plus 32 KiB; one chunk buffer per chunk, no larger than the chunk; and the library's fixed cost per LZMA chunk, which no rule limits, measured at about 200 bytes, or 20 bytes per input byte for chunks of 10 bytes, the smallest that hold data (`TestXZManyLZMAChunks`). Measured in the tests: under 1 MiB for a body refused before its block's data; the dictionary plus under 1 MiB for the review streams, refused at their second block, state reset or uncompressed run whatever their length (20,000 and 100,000 blocks; 1,000 and 200,000 state resets; 50,000 alternating runs; before these rules they allocated up to 785 GB, 2,960 MiB and 1,584 MiB); 12 MiB, 8 MiB of it the dictionary, for 500,000 consecutive one-byte uncompressed chunks (2 MB) in 36 ms. The fuzz target checks every input it tries against one dictionary plus 2 MiB plus 64 bytes per input byte |
| Extraction work | Bounded by the manifest limits and the 64-component path rule. The calls extraction makes to the operating system, and the path components in them, are linear in the number of folders and in files times depth; a test counts them, at depths within the rule and, below the path rules, at depth 1000. Folders are created and checked through their parent folder's handle; each file's path is resolved a few times. **Worst case at the default limits** (100,000 entries, 6,710,886 bytes; what a decode accepts without `--max-entries`). The manifest that maximises files times depth is a chain of 63 one-letter folders holding 23,773 empty files of 64 components, filling the default size limit; the entry limit does not bind. One extraction of it into a new folder, a single run on each CI runner (`BenchmarkExtractWorstCase`, nightly workflow run 43593004706, manual job `extraction-worst-case`); these are single measurements on shared runners, not bounds: ubuntu-24.04 2.5 s; ubuntu-24.04-arm 3.6 s; windows-2025 137 s; windows-11-arm 101 s; macos-26 35 s; macos-26-intel 88 s. The widest manifest at the defaults (43,026 empty files of one component) took 2.0 s, 2.5 s, 48 s, 45 s, 23 s and 65 s. **Worst case at the format limits**, which apply only with `--max-entries 1000000`: the same shape holds 237,274 empty files of 64 components and fills 64 MiB. Two single runs on each runner (runs 43519264222 and 43593004706), which show the variation between runs, up to a factor of 2.5 on ubuntu-24.04 and 1.2 on windows-2025: ubuntu-24.04 65 s and 26 s; ubuntu-24.04-arm 38 s and 37 s; windows-2025 1,179 s and 1,437 s; windows-11-arm 946 s and 998 s; macos-26 382 s and 353 s; macos-26-intel 600 s and 555 s. The widest manifest at the format limits (427,750 empty files of one component) took 46 and 22 s, 28 and 27 s, 384 and 478 s, 427 and 434 s, 403 and 337 s, and 548 and 621 s. On the slowest runner measured, windows-2025, a crafted transfer kept extraction busy for 137 s in its run at the defaults, and for 1,179 s and 1,437 s in its two runs with `--max-entries 1000000`. No memory figure is claimed for extraction beyond what section 9.5's resource test asserts. **Why the depth rule** (measured beyond what a decoder now accepts): 50 files beneath a chain of folders, extracted and then extracted again over itself, at depths 500 and 2000, took ubuntu-24.04 0.21 s and 1.3 s; ubuntu-24.04-arm 0.25 s and 1.3 s; windows-2025 7.4 s and 72 s; windows-11-arm 5.6 s and 67 s; macos-26 3.9 s and 78 s; macos-26-intel 4.7 s and 122 s. On Linux the time grows about linearly with depth; on Windows and macOS it grows 10 to 26 times for 4 times the depth, because resolving each component costs more in a deeper folder, so a deep manifest within the size limits could have kept extraction busy for hours. Progress is reported in every phase: entries checked, file bytes written, entries placed, folders finished. The operation can be cancelled while it checks the output folder, writes file data and places files: a cancelled extraction removes the staging folder and reverses everything it has placed. The last phase, which sets the modes and times of the folders it created after every file is in place, costs one change per folder and is not cancelled; a cancellation during it is ignored and the extraction succeeds |
| Soft-bit store | 256 MB |
| LDPC iterations per codeword | 25 |
| Sessions tracked per decode | 1 |
| Engine progress (`EngineStallTimeout`) | 30 s. An engine instance (decoding, probing, or copying a stream for the HEVC fast path) that makes no progress for 30 s is cancelled, and the decode stops with an error naming the limit. Progress is a complete output frame with its timestamp when decoding (a frame trickled a byte at a time is not progress), input read through the counting `fs.FS` view when probing, and growth of the output file when copying. Encoding and `Concat` are not covered: they run in qrvt's own process with close-on-cancel off and have no forced stop (T1). Time an instance spends waiting on qrvt (a full output pipe, an empty input pipe, a turn to run, the module's first compilation) is not a stall, except while qrvt is itself waiting on that instance for a frame's timestamp, a wait that also ends after 30 s. Ticks once a second, so a stall is stopped 30-31 s after the last progress. A decoding or probing instance runs in a worker process, which the watchdog kills; the worker's start and its loading and compiling of the engine are not counted (it reports when the engine is loaded; `EngineLoadLimit`, below, bounds that phase). In qrvt's own process (encoding, `Concat`, and the fallback when no worker can be started) at most GOMAXPROCS - 1 instances run at once (`video.EngineInstances()`, at least one). The cap was first justified by saying that compiled Wasm cannot be preempted; that is not so with `WithCloseOnContextDone(true)`, under which every loop back-edge of the compiled code returns to Go for its check, where the scheduler can preempt it. Without the cap the watchdog still stops every instance, but uncapped, stops were up to 8 times late on a CI runner: on macos-26-intel, GOMAXPROCS + 1 spinning instances took 16.8 s against a 2 s limit, while with the cap they stayed within its bound (on a 16-thread development machine GOMAXPROCS + 2 uncapped instances were stopped on time). The cap therefore stays as a real defence (`TestWatchdogUnderLoad` asserts the capped case and logs the uncapped one). Waiting for a turn has no limit but the command's context, so a caller that keeps that many decoders open without reading them waits for ever (`TestHoldAndWait`); on that in-process path qrvt's own decoding workers never hold more decoders open than the cap without reading them (DESIGN.md section 8). Worker processes are not capped. The margin: with close-on-cancel on, the engine decodes 4K HEVC at about 0.28 frames per second (about 3.6 s per frame, spike S1 section 10), so 30 s is about 8 times a 4K frame and, at four times the pixels, about 2 times an 8K frame; without close-on-cancel (about 0.3 s per 4K frame) it would be about 100 times. The worker processes of PR #15 run the engine in a child process with close-on-cancel off, removing it from the default path. This bounds stalls only, not total decode time; `MaxDecodedPixelsPerInputByte` bounds the work a file can demand. Implemented: `video.EngineStallTimeout` |
| Engine load (`EngineLoadLimit`) | 10 minutes from starting a worker process to its message that the engine is loaded and the program starts. That phase is not a stall: a first compilation of the engine on a slow machine can take longer than 30 s (it did on the macos-26-intel runner under the race detector, with several compiling at once), and starting a process on a loaded runner took over a second (windows-2025). A worker that has not sent the message by then is killed, and the job stops with an error naming the limit. The message is sent before any guest code runs, so a recording cannot withhold it. Compiling in qrvt's own process (encoding, `Concat`, the fallback) is not a stall either and has no limit but the command's context. Test: `TestWorkerLoadLimit`, with the limit shortened to 2 s, against a worker that never sends the message. Implemented: `video.EngineLoadLimit` |
| Probe (`EngineProbeTime`, `MaxProbeReads`) | 15 s of running time, not counting time waiting for a turn or for the module's first compilation, and at most 4 times the file's size plus 16 MiB read through the counting view, both checked once a second; beyond either the probe is cancelled with an error naming the limit. Measured with close-on-cancel on: probing the test clips, the four S1 pattern files and the six S2 iPhone recordings (up to 2.6 GB; H.264 and HEVC up to 4K; MP4, MOV, Matroska and MPEG-TS) took at most 1.2 s and read at most 1.07 times the file's size (MPEG-TS; the large MP4 and MOV files read under 1%), so 15 s is about 12 times the slowest. The format whitelist (T1) refuses at once the 110-byte HLS playlist that held ffprobe for more than 8 minutes; these bounds cover what the whitelist lets through. A native ffmpeg's reads are not counted; its probe has the time bound only. Tests built with the race detector use four times the time bound: there close-on-cancel's check at every loop back-edge runs as instrumented Go, and a 4-frame 640 x 480 probe exceeded 15 s on the windows-2025 runner with every package testing at once; no shipped build uses the race detector. Implemented: `video.EngineProbeTime`, `video.MaxProbeReads` |
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
  HLS playlist named `.mp4`. Each must end in a clear error within a time bound; the playlist must be
  refused by the format whitelist as soon as ffprobe opens it.

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

Worker processes (`worker_test.go`, written with them): a Wasm module that loops forever without
calling the host, in a worker, is killed by the parent within `EngineStallTimeout` plus 5 s with the
typed error and no process left; this kill is the control, because while a guest spins the worker's
own timers cannot be trusted (close-on-cancel is off in the worker, and a spinning guest blocks Go's
stop-the-world garbage collection there). A worker whose parent is killed mid-job exits by itself
within 5 s in the test, where the guest is the same spinning module; that exit, on its pipe closing,
is best effort and is not relied on. The test runs twice: with the worker in its Job Object and
without one. On Unix there is no Job Object, so both runs test the pipe exit. On Windows the run
without a job tests the pipe exit alone; the run with the job cannot tell which of the two ended the
worker. The Job Object is tested on its own (`TestWorkerJobObject`, Windows): the worker is created
suspended, assigned to its kill-on-close job and only then resumed; it then runs (it answers),
`IsProcessInJob` finds it in that job, and closing the job's handle kills it while its pipe from the
parent is still open. That the operating system closes the handle when qrvt crashes is Windows'
documented behaviour, not tested beyond the fake parent being killed. A worker that never reports
the engine loaded is killed at `EngineLoadLimit` (shortened in the test) with the limit named and no
process left (`TestWorkerLoadLimit`). On Unix a worker closes a descriptor it inherited without
close-on-exec (fd 9 in the test) as it starts, and still serves its job (`TestWorkerClosesInherited`).
A worker refuses a job of another protocol version, and the parent then runs the job in process and
reports why. More generally, whatever ends a worker before its ready message (an exit, a failure,
garbage, an early output message) is a failure to start: nothing the worker sent is passed on and the
job runs in process (`TestWorkerStartFailures`). After the ready message, the same refusal is an
ordinary failure (`TestWorkerVersion`,
`TestWorkerHostile`). Forged replies (a
message claiming 4 GiB, garbage, an unknown type, a malformed progress or exit message, an early exit,
a lying read count and escape sequences, error texts whose cut to their bound falls inside a
character, a partial frame) each end the job with an error, allocate under 64 MiB and print only
valid UTF-8 without control characters; a forged probe result (2e9 x 2e9 pixels) and a forged frame
size (65535 x 65535) are refused under the same allocation bound (`TestWorkerForgedResults`);
cancelling a decode and an encode is prompt and leaves no process and no file; cancelling a `Concat`
whose input stalls stops it (`TestConcatCancel`, Unix, with a FIFO); with no worker startable the
decode succeeds in process and `WorkerStatus` reports it. Workers write their coverage to the test's
coverage directory, so they count towards the 95% gate.

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
count 0, 1, 2 ... after the configuration line, frame lines before it are refused, and a second
configuration line is refused too (ffmpeg configures the filter once for a recording: it reconfigures
only when the frames change size, format or aspect ratio, and a size change stops the decode anyway).
Any of these fails the decode at its next frame.
`TestMetadataInjection` gives a clip container metadata shaped like a showinfo line and requires
unchanged timestamps; `TestCodecIDInjection` (the reviewer's case) gives a Matroska file's second track
a CodecID holding a newline and a showinfo line, and requires that no frame comes back mis-timed (the
decode fails); a second case (review round 3 of PR #9) puts a forged configuration line before the
forged frame, which before the second-configuration rule let a whole decode return its first frame at
the forged time and a slice end silently empty, and requires that the first frame, of a whole decode
and of a slice, already fails. `TestCaptureLimits` has the engine write a portrait-coded 4,320 x 7,680 clip, accepted,
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
random NAL units and a live HLS playlist named `.mp4` each end in `video.ErrInvalidVideo`; the test
requires the playlist's error to come from the format whitelist ("Format not on whitelist"), and
`TestProbeBound` shows that without the whitelist the probe's time bound stops it. The test asserts the
whitelist refusal rather than a wall time, which includes waiting for a slot behind the parallel tests
(11 s once under the race detector on a 4-core CI runner). The 1 GiB holds for these inputs. For a file at the capture limit it holds by
construction, not by test: a decoder holds about 640 MB, and about 1,020 MB with the whole reservation
resident, about 54 MB under 1 GiB (section 5).

**Exception: extracting a legal manifest.** Extraction of a manifest that is within the limits is
bounded by the manifest limits (section 5, "Manifest", "Path depth" and "Extraction work"), not by
60 seconds. In single runs on windows-2025, the slowest CI runner (section 5, "Extraction work",
with run numbers): 137 s at the default limits, and 1,179 s and 1,437 s in two runs with
`--max-entries 1000000`, the format limits. These are measurements, not bounds. During it,
progress is reported in every phase and the extraction can be cancelled. The 60-second bound applies
to everything before extraction: the manifest within the format limits that maximises files times
depth is validated and checked against the output folder within 1 GiB, and on Linux within 60
seconds (written in M1 stream A).

### 9.6 Static checks

The race detector on every pull request, CodeQL, `gosec` rules through the linter, and a test over the
root package, `cmd/` and `internal/` that fails if any package other than `internal/video` imports
`os/exec`, or if any package imports `unsafe`. Developer tooling under `tools/` and throwaway code under
`spikes/` are outside that rule, since they must launch other programs.

One reviewed exception: Windows system calls that take a struct's address (the Job Object's limits,
`IsProcessInJob`'s result in a test) get it from `reflect.Value.Pointer`, without importing `unsafe`.
That hand-off is one helper, `addr`, defined in `internal/video/worker_windows.go`. Being part of
package `video` on Windows, it may be called from any Windows file of `internal/video`, tests
included (`worker_windows_test.go` calls it for `IsProcessInJob`); no other package can call it. The
pointer is heap-allocated (passing it to reflect makes it escape), so it does not move, and each
caller keeps it alive across the call with `runtime.KeepAlive`. The same test fails if any other file
outside `tools/` and `spikes/`, whatever it imports, holds a selector named `Pointer`, `UnsafeAddr` or
`UnsafePointer`, called or not: a method call, a method value (`f := v.Pointer`), a method expression
(`reflect.Value.Pointer(v)`), or a call on a `reflect.Value` made in another package. Only
package-qualified names, such as the type `atomic.Pointer`, are allowed, and the test decides that by
type-checking each package (`go/types`): the left side must resolve to an imported package, so a
local variable or parameter named `reflect` or `atomic` does not pass. The fixtures include each of
these cases. The check is a guard against mistakes, not a proof: it matches names, so it does not
catch reflection by name (`MethodByName`) or an address formatted as text (`%p`) and parsed back.
Those are left to review.

## 10. Final security pass (milestone M7)

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
6. **Delta review before the release candidate (M8).** Code and workflows change after the pass. Before
   the release candidate is prepared, an independent reviewer examines everything merged since the pass
   ended (release workflows, packaging configuration, signing steps, and any code touched), with the
   same exit: no open finding of high or medium severity.

Severity: *high* breaks a guarantee in section 1; *medium* weakens a defence in section 4 without yet
breaking a guarantee, or is a denial of service that stays within the limits; *low* is everything else.

## 11. Reporting a vulnerability

Report privately through the repository's "Report a vulnerability" page (GitHub private vulnerability
reporting), not in a public issue. The root `SECURITY.md` created in milestone M8 repeats this
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
| 2026-11-14 | The owner swapped the last two milestones: the final security pass is M7 (section 10), and the root `SECURITY.md` comes with M8; M8 adds a delta security review of everything merged after the pass |
| 2026-11-13 | M1 stream D: T1 adds that the input view's files have no write methods and that the fast path exists only in `unsandboxedhevc` builds. Section 5: `EngineStallTimeout` counts a frame taken when encoding, ignores time spent waiting on qrvt, and relies on at most GOMAXPROCS - 1 instances running at once; `MaxDecodedPixelsPerInputByte` gives where the frame count comes from, that the headroom applies to recordings mostly showing the code, the trimming advice, and the work bound (about 5 instance-minutes per MB at 4K); the capture size is checked from the probe and size changes stop a decode; the fast path refuses parameter sets that differ from the probe and times each picture from its first slice. Sections 9.1, 9.3 and 9.5 name the implemented tests and their results |
| 2026-11-14 | M1 stream D, review of PR #9: T1 adds the protocol and format whitelists on every job that reads a recording (an HLS playlist named `.mp4` had held ffprobe for more than 8 minutes), the probe's bounds, closing the output pipe on cancellation, and the copy instance's size bound; the fast-path subsection states that bound. Section 5: decoding progress is a complete, timestamped frame, and qrvt waiting on an instance for a timestamp does not count as the instance waiting on qrvt; the instance cap is described as a precaution (with close-on-cancel, compiled loops return to Go at every back-edge; the CI delay that prompted it was more likely load), with the hold-and-wait hazard; new rows for the probe (`EngineProbeTime` 15 s, `MaxProbeReads` 4 x size + 16 MiB, with the measurements) and the fast path's copy size (`FastPathCopySize`, 2 x input + 64 MiB); the engine memory row gives a decoder's host memory at the capture limit (201 MB of planes, the frame-sized pipe buffer removed); odd picture sizes are refused. Section 9.1: the fuzz task builds with the fast-path tag. Section 9.2 adds the HLS playlist; section 9.3 names the decoder-stall, metadata-injection, capture-limit and new fast-path tests; section 9.5 states where the 1 GiB is and is not shown |
| 2026-11-14 | M1 stream D, CI on PR #9: section 5 corrects the instance cap's description: without it the watchdog still stops every instance, but on the macos-26-intel runner GOMAXPROCS + 1 uncapped spinning instances took 16.8 s against a 2 s limit, so the cap is a mitigation, not only a precaution |
| 2026-11-14 | M1 stream D, round 2 of the review of PR #9: section 5 lowers the capture limit to 8K UHD in either orientation (longer side 7,680, shorter side 4,320), restates a decoder's host memory at it (99.5 MB of planes; about 1,020 MB in the worst case, inside section 9.5's 1 GiB), records the cap's evidence (uncapped, stops were up to 8 times late on a CI runner) and restates the stall margin with close-on-cancel (about 8 times a 4K frame, 2 times an 8K frame; PR #15 removes close-on-cancel from the default path). Section 9.3 corrects the injection wording (text logged from the start of a line, such as a Matroska CodecID, passes the anchored match; frame lines must count in sequence) and names `TestCodecIDInjection` and the orientation cases; section 9.5 states that the 1 GiB holds at the capture limit by construction |
| 2026-11-14 | M1 stream D, CI on PR #9: sections 9.2 and 9.5 require the HLS playlist to be refused by the format whitelist, instead of within 5 s of wall time, which includes waiting for an instance slot |
| 2026-11-14 | M1 stream D, CI on PR #9: section 5 states that tests built with the race detector use four times `EngineProbeTime`, and why |
| 2026-11-13 | M1 stream D, worker processes: guarantee 1's basis includes the worker process; T1, section 5 and section 9.3 describe the worker, its fallback, its exit when the parent goes, and that it is not an operating-system sandbox |
| 2026-11-14 | M1 stream D, review round 3 of PR #9: section 9.3 adds that a second showinfo configuration line is refused, and the test with a forged configuration line before a forged frame |
| 2026-11-14 | M1 stream D, review of PR #15: T1 states that encoding and `Concat` run in qrvt's own process with close-on-cancel off and no forced stop (acceptable because their input is qrvt's own), that cancelling them closes their input, and that their guest delays, and a spinning one would stall, qrvt's own garbage collection, which matters to a host that encodes and decodes in one process; it adds the protocol version refusal, the cut on a character boundary and `EngineLoadLimit`. Section 5: a new row for `EngineLoadLimit` (10 minutes, with its reasons and test); the stall row no longer lists encoding among the progress kinds, says encoding and `Concat` are not covered, and limits "never exceed the cap" to the in-process path. Section 9.3 claims only what is tested: the orphan test with and without the Job Object, the Job Object test (suspended start, `IsProcessInJob`, kill on closing the job), the load-limit, inherited-descriptor, protocol-version, forged-result and `Concat` cancel tests. Section 9.6 records the `reflect` address hand-off as the one reviewed exception, checked by the import-rule test |
| 2026-11-14 | M1 stream A, payload limits: section 5's "Extraction work" row gives the worst case at the default limits as measured on the six CI runners (23,773 files of 64 components: 2.5 s to 137 s, windows-2025 the slowest) and says the earlier worst case (237,274 files) applies only with `--max-entries 1000000`, with a second run of it (up to 1,437 s on windows-2025). Section 9.5's exception gives both figures; the open decision on lowering the limits is removed, the owner having set the defaults |
| 2026-11-14 | M1 stream A, payload limits, the designer's wording: section 5's "Extraction work" row and section 9.5 give the extraction times as single runs with their run numbers and the variation seen between runs, not as bounds, and claim no memory figure for extraction beyond what the section 9.5 resource test asserts |
| 2026-11-14 | M1 stream A, review of PR #19: section 5's Manifest row says the size limit usually binds before the entry limit, about 40,000 typical entries at the defaults (43,026 in the widest manifest measured) |
| 2026-11-14 | M1 stream D, round 2 of the review of PR #15: section 9.3 says that whatever ends a worker before its ready message is a failure to start, with the job run in process (`TestWorkerStartFailures`). Section 9.6 says `addr` may be called from any Windows file of `internal/video`, tests included, and that the import-rule test refuses any selector named `Pointer`, `UnsafeAddr` or `UnsafePointer` outside the exception file, called or not and whatever the file imports (method values, method expressions, values made in another package), allowing only package-qualified names |
| 2026-11-14 | M1 stream D, review of PR #20: section 9.6 says the address rule is decided by type-checking each package, so a local variable or parameter named after an imported package does not pass, and that the check guards against mistakes and is not a proof: reflection by name (`MethodByName`) and addresses formatted as text (`%p`) are left to review |
