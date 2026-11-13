# qrvt design

qrvt turns files into a looping video. You play the video full-screen, film the screen with a phone, copy
the phone's recording to another machine, and qrvt turns the recording back into the files. Nothing but
light crosses between the two machines.

This document explains the design and why each choice was made. Exact bit layouts are in
[FORMAT.md](FORMAT.md), the threat model in [SECURITY.md](SECURITY.md), and the build plan in
[IMPLEMENTATION_PROMPT.md](IMPLEMENTATION_PROMPT.md).

**How much to trust the numbers.** Figures marked *simulated* come from about 110 runs through real
H.264 and HEVC encoders with a modelled camera. Figures marked *estimated* are arithmetic on top of those.
Nothing here has been measured on a phone yet. Steps S1 and S2 of the build plan exist to replace these
figures with measurements before anything is frozen.

## 1. Requirements

| Topic | Decision |
| --- | --- |
| File sizes | 1-50 MB typical; 1 GB is a first-class goal |
| Inputs | Any number of files and directories |
| Output | One H.264 (8-bit 4:2:0) MP4 that plays in any stock player and loops |
| Resolution and rate | Presets 480p, 720p, 1080p, 1440p, 4K at 12, 30 or 60 fps; arbitrary values allowed |
| Modes | Greyscale (default), black-and-white, colour, experimental HDR output; HDR recordings read at full precision |
| Progress | Payload length and frame count travel in the video, so every progress bar is exact |
| Capture | Handheld by design; a tripod unlocks denser settings |
| Recording devices | iPhone, Android flagship, older Android (1080p30), other cameras |
| Camera frame rate | Unknown in advance, so `calibrate` measures and recommends |
| Binary | Static, no runtime dependencies, Linux, macOS and Windows on amd64 and arm64 |
| Video engine | ffmpeg embedded as WebAssembly; a native ffmpeg is an opt-in speed-up |
| Licence | GPL-3.0-or-later, which follows from embedding ffmpeg with x264 |
| Extras | Optional passphrase encryption, optional signing, merging several recordings |
| Out of scope for now | Top-up videos, live camera decoding, a built-in player, quarantine flags, antivirus hooks |
| Quality | Test-driven, at least 95% statement coverage, concise documented code |
| Later | A desktop GUI on a stack not yet chosen |

## 2. Overview

```
ENCODE  files/dirs -> manifest + tar -> compress -> [encrypt] -> [sign] -> stream
        -> shards -> Reed-Solomon parity, spread over the whole loop
        -> per frame: LDPC codewords -> interleave -> scramble -> cells
        -> frame layout -> H.264 / MP4 through the embedded engine

DECODE  recording(s) -> embedded engine, one instance per CPU core -> raw frames
        -> per capture: find the code -> correct geometry -> sample cells -> calibrate levels
        -> decide which code frame each cell shows -> soft bits
        -> add soft bits per code frame across captures -> LDPC -> CRC
        -> Reed-Solomon -> verify hash [and signature] -> [decrypt] -> decompress -> unpack
```

Terms: a **code frame** is one image of the encoded video; a **capture** is one frame of the recording.
A **soft bit** is a bit together with a confidence, written as a log-likelihood ratio (LLR): large and
positive means "surely 0", large and negative "surely 1", zero "no idea". **LDPC** is an error-correcting
code that uses those confidences. A **shard** is a fixed-size piece of the data stream.

## 3. Go, and when to reconsider Rust

The tool is written in Go. Security was the reason Rust was raised, and it does not separate the two:

| Question | Go | Rust |
| --- | --- | --- |
| Memory safety | Safe, except that a data race can corrupt memory | Safe, and data races are ruled out at compile time |
| Official guidance | NSA/CISA list both as memory-safe languages | Same |
| Parsing untrusted video | ffmpeg inside the wazero sandbox; no published sandbox advisories, though less scrutinised | wasmtime is heavily fuzzed but had several sandbox-escape CVEs in 2024-26, two rated critical in October 2026. The pure-Rust H.264 and HEVC decoders are all under 8 months old with single maintainers |
| Unpacking and crypto | Standard-library tar; the reference age implementation | tar crate: two CVEs in 2026. zstd binding (C code): three advisories in October 2026. age crate still labelled beta |
| Static binary on six targets | Pure Go, no C in the process | wasmtime is lowest-tier on Linux-musl and Windows-arm64; C libraries need a toolchain per target |
| Decode speed | wazero about 4.7x slower than native in the one benchmark found (cryptography, not video) | wasmtime about 2.4x in the same benchmark |
| Tooling | Fuzzing and coverage built in; GoReleaser covers every release channel | Good tools, more assembly; fuzzing needs the nightly compiler |

Go's one real gap is data races. Two measures close it: the race detector runs on every pull request,
and workers share no mutable state and communicate only through channels.

Rust's real advantage is speed. Spike S1 therefore runs the same ffmpeg module under wasmtime's
command-line runner beside wazero, which needs no Rust code. If wazero misses its decode threshold *and*
wasmtime is at least twice as fast, work stops and the numbers go to the project owner. At that point
only scaffolding exists, and the format, the simulator design and this document are language-neutral.

## 4. What limits throughput

The intuitive answer is camera resolution. Simulation says otherwise.

**Method (simulated).** Random cells were encoded with x264 intra-only as the display video, resampled to
*s* camera pixels per cell with per-frame half-pixel jitter, blurred (Gaussian, sigma 1.0 pixel; 1.5 for
handheld), given noise of plus or minus 4 code values, reduced to 4:2:0, and encoded with x265 at
constant quantiser as a stand-in for the phone (x264 for phones that record H.264). Each code frame was
captured twice. Cells were then sampled and classified, and the information actually delivered per cell
was computed.

**Not modelled:** moire, phone sharpening and noise reduction, tone mapping, HDR, mixed frames, real
optics. The ratios between options are more trustworthy than the absolute values.

| | Finding | Evidence |
| --- | --- | --- |
| F1 | The recording's bitrate is the ceiling. Delivered bits per cell depend on *q*, the recorder's bits per cell per code frame, almost regardless of cell size or captures per frame | Four grey levels, HEVC: q = 2.3 gives 0.78 bit; 4.1 gives 1.26; 5.9 gives 1.68; 7.5 gives 1.87; 9 gives 1.95. One, two or four captures per frame at fixed quantiser: 1.19, 1.26, 1.29 bit |
| F2 | At each one's best cell size, all sensible modulations are within about 20% | Delivered bits per camera pixel per frame on a 4K HEVC phone: four greys 0.109, eight colours 0.093, layered 0.093, two greys 0.076 |
| F3 | The display video's size depends on aligning cells to H.264's 4x4 transform blocks | Bits per cell at quantiser 32: four greys at 4 pixels 8.4; at 8 pixels 10.1; at 3 pixels 29.1; at 6 pixels 26.3; eight colours at 4 pixels 28.1. No cell errors through the display codec up to quantiser 38 |
| F4 | Blur moves the best cell size by about one camera pixel | Sigma 1.0 to 1.5: four greys at 5.1 camera pixels per cell drop from 1.7 to 1.5 bits |

**Rule of thumb:** net throughput is 13-16% of the recording's bitrate. An iPhone recording 4K60 HEVC at
about 53 Mbit/s therefore carries roughly 0.9 MB/s at best.

Consequences that shaped the design:

- Cells are always a multiple of 4 video pixels (4 by default), aligned to the transform grid. Density
  is changed by changing the video's resolution, never by using 3- or 6-pixel cells.
- A faster display rate only helps while the camera still has bits to spare.
- The single most effective speed-up for a user is a higher-bitrate recording mode.

## 5. Modulation

**CMYK does not apply to a screen.** It describes ink absorbing light. A screen emits red, green and blue;
cyan, magenta and yellow are pairs of those. The eight corners of the RGB cube are K, R, G, B, C, M, Y, W,
so an "RGB or CMYK" code is an 8-colour cell carrying 3 bits. That is the `color` mode.

| Mode | Flag | Bits per cell | Camera pixels per cell needed, steady / handheld (simulated) | Display bits per cell (simulated) | Role |
| --- | --- | --- | --- | --- | --- |
| Four greys | `--mode gray4` | 2 | 3.5 / 4.5 | 8.4 | Provisional default |
| Black and white | `--mode bw` | 1 | 2.6 / 3.4 | 7.3 | Most robust |
| Eight colours | `--mode color` | 3 | 4.3 / 5.5-6 | 28 | Best when the recording bitrate is generous |
| Eight greys, 10-bit | `--hdr` | 3 | not simulated | not simulated | Experimental |

Why four greys is the provisional default:

- It delivered the most per recorded bit: 0.29-0.36 against 0.23-0.28 for eight colours.
- Its video is about a third the size of the colour video.
- Calibration is one-dimensional, and it is immune to colour subsampling, colour noise reduction, white
  balance, Night Shift and True Tone.
- The decoder needs only the brightness plane, which makes video decoding cheaper.

Its weak points are tone mapping and highlight clipping, which were not simulated. **The default is not
frozen until step S2 measures it on real phones.** If grey levels merge there, the default becomes
black-and-white or colour, whichever measures best. The mode travels in every frame's header, so
changing the default never breaks existing videos.

Colour stays because it wins when the recorder is not bitrate-starved (q above about 15: 1080p H.264 at
17 Mbit/s, ProRes, mirrorless cameras), where it delivered about 2.9 bits per cell against 2.0.

**HDR** works in two directions:

- *Recordings* made in HDR, which is the iPhone default, are decoded to 10-bit samples and used at full
  precision in every mode. No tone mapping is applied; the monotone transfer curve is absorbed by level
  calibration.
- *Output* with `--hdr` writes a 10-bit video with eight grey levels. The embedded engine has no HEVC
  encoder, so spike S1 chooses between AV1 10-bit, which is already embedded, and adding an encoder.
  This mode cannot play in every stock player and is labelled experimental.

Considered and rejected:

| Technique | Why not |
| --- | --- |
| Colour QR codes | About 3 KB per symbol, heavy fixed patterns, and byte-oriented hard-decision Reed-Solomon. Camera errors are typically 1-3 bits, which such codes handle poorly |
| Brightness at full resolution plus colour at half | No simulated gain over plain cells; the RGB gamut squeezes the colour swing to about 32 code values once brightness also carries data |
| Shaped tiles (libcimbar) | A 16-shape alphabet over 64 pixels is a weak code. It suits live decoding on a phone CPU, which is not needed here |
| Frequency-domain (OFDM) | Needs amplitude linearity, which gamma and tone mapping break, and sub-pixel coherence, which rolling shutter and lens distortion break. The recorder's quantiser also removes exactly the faint high-frequency carriers |

## 6. Frame layout

The frame has two layers.

- The **structure layer** (border, four finders, four header copies, four palette strips) is defined as
  fractions of the screen, the same for every resolution. A decoder finds it and reads the header without
  knowing anything about the grid.
- The **data layer** (anchors and data cells) is defined on the cell grid, whose size the header announces.

At 1080p (480 x 270 cells):

| Element | Purpose | Share |
| --- | --- | --- |
| Mid-grey border, 2 cells | Separates the code from the bezel and player controls without biasing exposure | 2.3% |
| Four finders, 14 x 14 cells plus separator | Concentric squares giving a 1:1:3:1:1 signature at any scale or angle. The bottom-right one is inverted to fix rotation and mirroring | 0.8% |
| Four header copies, 59 x 4 slots of 2 x 2 cells | Frame identity. Four copies because a capture can show two code frames at once | 2.9% |
| Four palette strips, 8 patches | A reference patch for every level | 0.4% |
| 131 anchors every 32 x 30 cells | A checker corner for sub-pixel geometry and a four-quadrant phase marker | 3.2% |
| Data cells | 117,112 of 129,600 | 90.4% |

Design notes:

- There are no timing tracks. The anchor lattice gives a denser and more robust correction mesh.
- Data is scrambled with a per-frame pseudo-random sequence. That keeps average brightness constant
  (steady auto-exposure), removes regular patterns (less moire), makes every level equally frequent
  (so level centres can be learned from the data itself), and makes consecutive frames differ everywhere
  (so mixing is detectable).
- A player may scale or letterbox the video. The decoder never assumes pixel alignment; the only rule is
  that a cell must cover at least 3 physical screen pixels. A video larger than the screen's native
  resolution is downscaled by the player and loses cells, so `calibrate` checks for it.

### Header and descriptor

The **header** is 112 bits: format version, session id, frame index, total frames, grid size, mode,
protection level and a CRC. It is protected by a rate-1/2 convolutional code and drawn in large
black-and-white slots. The decoder samples the four copies at fixed fractions of the finder rectangle,
decodes each alone first (copies in one capture may belong to different code frames), then in pairs,
then all four.

The **stream descriptor** is 59 bytes carried as the first codeword of every frame: full session id,
stream length in bytes, shard and block sizes, block count, total frames, a seed, and a hash of the
stream. It is identical in every frame, so its soft bits can be summed across frames until it decodes.

Together they give exact progress from the first readable capture: frames seen of total, bytes recovered
of total, blocks complete, and time remaining. On the encode side the payload is packed and compressed
to a spool file first, so its length is known before the first frame is drawn.

## 7. Time

A phone sensor reads its rows over 5-30 ms. An LCD refreshes top to bottom over about one refresh period.
Exposure time and pixel response widen the transition further. A capture therefore often shows frame *n*
in one region and *n+1* in another, with a blended band between. The boundary is horizontal only if
phone and screen scan in the same direction; with the phone rotated it is vertical or diagonal.

With grey levels this is dangerous, not merely noisy: a half-and-half blend of black and white looks like
a valid mid-grey. Mixed cells must be discarded reliably.

Rules for version 1:

1. **Identity.** A capture is usable if at least one header copy passes its CRC with a matching session.
   If two copies give different frame indices, both are kept. If none decodes, the index may be inferred
   as the last index plus 0 to 3, chosen by the phase markers, only when fewer than four frame periods
   have passed. Otherwise the capture is skipped.
2. **Phase field.** At each anchor, the four quadrant intensities are linearised using the anchor's own
   black and white and the palette's gamma, then solved for non-negative weights summing to one, one per
   value of frame index mod 4. Interpolating across the lattice gives per-cell weights. This handles
   boundaries in any direction, player tearing and three-frame blends.
3. **Assignment.** A cell contributes to code frame *k* only if the weight for *k* mod 4 is at least 0.92.
   Otherwise its soft bits are zero.
4. **Guarantee.** With camera interval *Tc*, exposure *E*, pixel response *tr* and display period *Td*,
   every cell of every code frame is captured cleanly in one pass if *Tc + E + tr <= Td*. That holds for
   12 fps on a 30 fps camera with 50 ms to spare, and for 30 fps on a 60 fps camera when *E + tr* is
   under 16.7 ms.
5. **Slow cameras.** When the camera is slower than twice the display rate, the mixed band is discarded in
   each capture. Its position drifts between loop passes, so several passes fill in. `decode` reports the
   clean fraction and recommends a lower display rate. Single-pass success is not promised.
6. **Loop wrap.** The frame count is padded to a multiple of 4 so the phase sequence is continuous across
   the seam. Black frames or a freeze at the seam simply fail finder detection.
7. **Duplicates.** Soft bits are added. The first capture of a code frame in a pass has weight 1; adjacent
   captures of the same frame have weight about 0.5, because their codec noise is correlated. Other
   passes and other recordings have weight 1. This is what makes merging recordings work.
8. **Drops.** A code frame never seen becomes missing shards for the outer code.
9. **Variable frame rate.** Timestamps are never trusted for identity, only for the bound in rule 1 and
   for diagnostics.
10. **Long exposure.** If no region of a capture reaches a weight of 0.92, the capture is unusable and an
    `ExposureTooLong` warning is raised.
11. **When to decode.** LDPC is attempted for code frame *k* once two captures have passed without it.
    Codewords that fail keep their summed soft bits (8-bit, capped at 256 MB, least confident evicted
    first) for later passes or recordings.

| Display rate | Camera needed | Status |
| --- | --- | --- |
| 12 fps | 30 fps | Default; works with every device |
| 30 fps | 60 fps | Supported |
| 60 fps | 120 fps and a fast panel | Experimental: a 60 Hz panel is mid-refresh almost all the time |

Deferred to a later version: recovering mixed bands by subtracting an already-decoded neighbouring frame,
and refining the geometry mesh from decoded data.

## 8. Error correction

There are two kinds of damage, so there are two codes.

**Inside a frame: LDPC.** The inner code is the IEEE 802.11n quasi-cyclic LDPC code of length 1944,
shortened so that each codeword carries exactly one shard and a CRC-32C.

| `--protection` | Shard | Net rate | Survives roughly (estimated) |
| --- | --- | --- | --- |
| `fast` | 192 bytes | 0.81 | 3% of cells unreadable |
| `normal` (default) | 128 bytes | 0.66 | 15% |
| `robust` | 64 bytes | 0.34 | 50% |

- Decoder: layered normalised min-sum, scaling 0.75, 8-bit soft bits, at most 25 iterations, stopping
  early when all checks pass, then the CRC.
- The matrices must come from the standard, not from memory. A wrong table still gives a
  self-consistent code, so the guard is a bit-error-rate test against published thresholds.
- Codeword bits are spread over the whole frame by a pseudo-random permutation with a per-frame rotation,
  so glare in one corner costs every codeword a little instead of a few codewords everything.
- No mature Go library does soft-decision LDPC, so this is written in the repository. The DVB-S2
  short-frame code (16,200 bits) would give about 0.5 dB more and can be added later behind the same
  interface.

**Across frames: Reed-Solomon.** The outer code is Leopard Reed-Solomon over GF(2^16) from
`klauspost/reedsolomon`, one shard per LDPC codeword.

- Up to 32,768 data shards per block (2-6 MB), all blocks the same size, default 20% parity.
- Shards are sent in a pseudo-random order that interleaves all blocks across the whole loop. A lost
  frame costs each block at most one shard once there are at least as many blocks as codewords per frame.
- With 20% parity, any 83% of the loop is enough. `--parity` accepts 5 to 100.

Why not a fountain code: the loop is a fixed set of frames, so an endless supply of new symbols is no
use without top-up videos. Reed-Solomon recovers a block from any sufficient subset with zero overhead,
and the Go library is mature where the fountain libraries are months old. Repeat passes still help,
through soft-bit summing, which a fountain code would not give.

**Memory.** One Reed-Solomon worker holds one block: at most about 13 MB plus scratch, bounded at 32 MB.
Shards live in a temporary file of (1 + parity) times the stream size, on both the encode and the decode
side. qrvt's own memory, not counting engine instances, is targeted at 600 MB at 1080p and 1.5 GB at 4K.
Each engine instance adds what spike S1 measures, with a ceiling of 600 MB when decoding 4K. The default
worker count is the CPU count, reduced so that all engine instances together stay within half of the
machine's physical memory.

## 9. Payload

1. **Pack.** A JSON manifest (path, size, mode, time and SHA-256 of every file) followed by a tar stream.
   Only regular files and directories are packed; links and special files are skipped with a warning.
   Each input is stored under its base name. Two inputs with the same name, or a name the path rules
   forbid, stop the encode with an error; nothing is renamed silently.
2. **Compress.** zstd at its strongest pure-Go setting by default; `--compress xz` for the best ratio;
   stored as-is when a sample shows the data does not compress. Every megabyte saved is seconds of filming.
3. **Encrypt** (optional). The age format with a passphrase.
4. **Sign** (optional). An Ed25519 signature over the header and body, with the public key alongside.
5. **Stream.** A 24-byte header, the body, and the signature block.

The manifest lets the decoder validate every path and the total size before writing anything, and lets
unpacking show exact progress.

## 10. Decoder

For each capture:

1. **Detect.** On brightness downscaled to at most 1280 pixels wide, scan for the finder signature. To
   stay independent of what surrounds a finder, match the inner light, dark (3), light pattern and
   require only that the flanks are dark for at least half a module (inverted for the fourth finder).
   Choose four candidates with consistent module size. Track from the previous capture when possible.
2. **Read the header.** Fit a homography to the four finder centres and sample the header slots at their
   fixed fractions, using the adjacent finder's black and white as references. Soft Viterbi decoding per
   copy, then per pair, then all four.
3. **Fit the mesh.** Locate every anchor's checker corner to sub-pixel accuracy, reject outliers, and
   interpolate. This absorbs lens distortion, rolling-shutter shear and stabilisation warping.
4. **Sample.** Take the mean of the central 60% of each cell's footprint directly from the camera planes.
   The image is never warped whole.
5. **Calibrate levels.** Per tile of about 32 x 30 cells, keep a centre and variance for each level.
   Start from the palette and the anchors' black and white, refine with two or three rounds of
   expectation-maximisation on the data cells (scrambling makes levels equally likely, and their order
   resolves the labels), and interpolate between tiles.
6. **Cancel stripes.** Subtract each cell's decided level, take a 2-D Fourier transform of what remains,
   remove peaks standing above the noise, and re-estimate. Moire and backlight flicker are a few plane
   waves and show up as such peaks.
7. **Soft bits.** For each bit, the difference between the best matching level with that bit 0 and with
   that bit 1, scaled by the variance, as a clipped 8-bit value. Saturated cells give zero.
8. **Assign and sum.** Apply the phase rules of section 7 and add into the code frame's accumulator.

| Distortion | Handled by |
| --- | --- |
| Perspective, rotation, mirroring | Finders and homography |
| Lens distortion, shake, stabilisation warping | Anchor mesh, refitted for every capture |
| Moire, backlight flicker | Footprint averaging, scrambling, stripe cancellation |
| Exposure, white balance, tone mapping, vignetting | Per-tile level calibration |
| Glare, reflections, on-screen overlays | Saturated cells become unknown |
| Blur, focus hunting | Variance-weighted summing |
| Mixed frames | Phase field |
| Compression artefacts | Soft-decision LDPC |

**Merging and resuming.** Accumulators and recovered shards are keyed by session and frame index, so
several recordings passed to one `decode` simply add up. With `--state-dir`, recovered shards and
unresolved soft bits persist, and a later run reads only the new recording.

## 11. Expected performance

Net bytes per frame are fixed by the format: `(C - 1) x shard size x K / (K + M)`, where C is codewords
per frame. At 20% parity:

| Preset | Data cells | `fast` | `normal` | `robust` |
| --- | --- | --- | --- | --- |
| 480p | 22,848 | 3.6 KB | 2.9 KB | 1.5 KB |
| 720p | 51,540 | 8.3 KB | 6.8 KB | 3.4 KB |
| 1080p | 117,112 | 19.1 KB | 15.6 KB | 8.0 KB |
| 1440p | 207,948 | 34.1 KB | 27.9 KB | 14.2 KB |
| 4K | 469,088 | 77.2 KB | 63.2 KB | 32.1 KB |

(Four greys. Black-and-white carries half, colour one and a half times.)

Whether a setting works depends on the camera. All of these must hold (simulated thresholds):

1. Camera frame rate at least twice the display rate.
2. Camera pixels per cell `s = fill x camera width / GridX` at least the mode's minimum, with fill 0.80
   handheld and 0.90 on a tripod.
3. Recorder bits per cell per code frame `q = 0.9 x bitrate / (rate x cells)` at least 9 for `fast`,
   5.5 for `normal`, 2.5 for `robust` with HEVC; 1.4 times those with H.264.
4. The display video's bitrate, `8.4 x cells x rate`, stays playable: 33 Mbit/s for 1080p30,
   58 for 1440p30, 131 for 4K30.

Planning figures are the simulated single-pass rate derated by 40% for everything not simulated:

| Camera | Recommended | Planning rate | 10 MB | 1 GB |
| --- | --- | --- | --- | --- |
| 1080p30 (older Android, webcam) | 720p at 12 fps | 50-60 KB/s | about 3 min | 4-5 h, impractical |
| 4K30 | 1440p at 12 fps | about 200 KB/s | about 50 s | about 80 min |
| 4K60 with a 1080p monitor | 1080p at 30 fps, `fast` | about 350 KB/s | about 30 s | about 47 min |
| 4K60 | 1440p at 30 fps | about 500 KB/s | about 20 s | about 32 min |
| 4K120 | 1440p at 30-60 fps | 0.6-1.0 MB/s | 10-16 s | 16-26 min |

Assumed recording bitrates: 1080p30 at 8 Mbit/s HEVC or 17 H.264; 4K30 at 23; 4K60 at 53; 4K120 at about
110. For comparison, libcimbar reports about 106 KB/s.

Costs a user should expect:

- The video is about 8 times the size of the compressed payload at `normal`: 10 MB becomes about 85 MB,
  1 GB about 8.5 GB. The phone's recording of a 1 GB transfer is about 13 GB.
- A full 4K grid at 60 fps is impractical: its video would need about 260 Mbit/s, beyond what stock
  players sustain, and no phone records enough bits to resolve it. 4K output remains available with
  larger cells, where it carries what 1080p does.
- Decoding with the embedded engine is expected to take 1-3 times the recording's duration at 4K on
  8 cores. `--ffmpeg system` should bring that near real time.

## 12. Video engine

`codeberg.org/gruf/go-ffmpreg` packages ffmpeg 5.1 with x264 as a WebAssembly module, run by `wazero`.

- **Writing.** x264, High profile, intra-only, CABAC, quantiser about 32, BT.709 limited range. Workers
  each encode a run of frames to an elementary stream; all-intra streams with identical parameter sets
  concatenate validly, and one final copy pass wraps them in MP4.
- **Reading.** Each worker decodes its own time slice to raw frames, without auto-rotation, brightness
  only for grey modes. Captures identify themselves, so slices overlap freely and order does not matter.
- **Backends.** One interface with three implementations: embedded (default), an external `ffmpeg`
  process for `--ffmpeg system`, and a raw-frame file backend for tests.
- **Start-up.** The compiled module is cached on disk, so only the first run pays for compilation.
- **Sandbox.** When decoding, the module sees only the input file, read-only, where the runtime allows
  that, and otherwise the file's directory read-only. When encoding it sees one output directory. It
  has no network and a memory ceiling.

Rejected as a foundation: the pure-Go H.264 stack (`oops1/go.264`, `go-avkit/*`). An independent check
found it is one author's 2.5-month-old project that links glibc on Linux through purego and silently
switches to hardware codecs on Windows unless built with special tags, and that `go-avkit/avkit` loads
every sample of a file into memory.

Fallbacks if the spike shows the embedded engine is too slow:

- *Encode*: a small in-repository H.264 writer. Because every cell is exactly one 4x4 block of constant
  value, only DC coefficients are needed. `Eyevinn/hi264` demonstrates the approach for 8- and 16-pixel
  blocks.
- *Decode*: `gen2brain/h265`, a pure-Go HEVC decoder with outside contributors and conformance tests.

## 13. Command line

```
qrvt encode <path>... -o out.mp4
qrvt decode <recording>... -o dir
qrvt calibrate make -o cal.mp4
qrvt calibrate read <recording>
qrvt inspect <video|recording>
qrvt keygen -o name
qrvt profiles | version | completion | man
```

| Flag | Command | Default | Meaning |
| --- | --- | --- | --- |
| `--resolution` | encode | `1080p` | Preset or `WxH` |
| `--fps` | encode | `12` | Display frame rate |
| `--mode` | encode | `gray4` | `gray4`, `bw` or `color` |
| `--hdr` | encode | off | Experimental 10-bit output |
| `--protection` | encode | `normal` | `fast`, `normal` or `robust` |
| `--parity` | encode | `20` | Parity shards as a percentage of data shards, 5-100 |
| `--cell` | encode | `4` | Cell size in pixels: 4, 8, 12 or 16. The video size is reduced to a multiple of it, and the size used is reported |
| `--compress` | encode | `auto` | `auto`, `zstd`, `xz` or `none` |
| `--encrypt` | encode | off | Encrypt with a passphrase, prompted for unless `--passphrase-file` is given |
| `--sign-key` | encode | none | Sign with this private key |
| `--dry-run` | encode | off | Print video length, file size and expected filming time, then stop |
| `--passphrase-file` | encode, decode | none | Read the passphrase from a file instead of prompting |
| `--verify-key` | decode | none | Trusted public key; may be repeated. When given, the transfer must be signed by one of them |
| `--max-size` | decode | 16 GiB | Largest total size that may be unpacked |
| `--overwrite` | decode | off | Allow existing files in the output folder to be replaced |
| `--state-dir` | decode | none | Keep progress for resuming and merging |
| `--report` | decode | none | Write a JSON report of the decode |
| `--ffmpeg` | all | `embedded` | `embedded`, `system` or a path |
| `--jobs` | all | CPU count | Worker count |
| `--json`, `--plain`, `--quiet`, `--verbose` | all | | Output style |

- **Dashboard.** Built with Charm v2 (bubbletea, bubbles, lipgloss): source, video and signal panels, one
  bar per stage, rate and time remaining, and on decode a block map that fills in. When output is not a
  terminal it prints plain lines; `--json` prints one JSON event per line.
- **Help.** Each flag has a long description, default, range and example. `--help`, the man page and the
  website are generated from one definition (cobra with fang), so they cannot drift.
- **Failure guidance.** A decode that falls short says what is missing and what to do, for example
  "2.1% short: record one more loop and pass both files".
- **calibrate.** `make` writes a test video stepping through modes, densities and display rates. `read`
  measures each step in a recording and prints a table with the recommended `encode` command, plus
  warnings for clipping, dropped frames, moire and exposure.

## 14. Architecture

```
qrvt (module root)   Public API
cmd/qrvt             main only
internal/cli         cobra commands and flags; ui/ holds dashboard, plain and NDJSON renderers
internal/payload     manifest, tar, compression, age, keys and signing, stream format, safe extraction
internal/fec         interfaces, block plan, transmission order
internal/fec/ldpc    inner code
internal/fec/conv    header code: convolutional encoder and Viterbi decoder
internal/fec/rs      Reed-Solomon codec, disk-backed shard store
internal/frame       layout geometry, header and descriptor wire formats, bits-to-cells mapping
internal/modem       constellations, bit mapping, soft bits
internal/render      code frame to image planes
internal/vision      detector, rectifier, sampler, equaliser, phase estimator
internal/video       ffwasm, ffsys and rawvid backends
internal/channel     simulated camera and metrics
internal/pipeline    orchestration, worker pools, soft-bit store
internal/calib       calibration video, analysis, recommendation
tools/dev            task runner behind the scripts
```

Only the root package is public. It is named `qrvt` although the module path ends in `qrvideotransfer`;
that is intended. Its surface:

```go
package qrvt

func Encode(ctx context.Context, o EncodeOptions) (*EncodeReport, error)
func Decode(ctx context.Context, o DecodeOptions) (*DecodeReport, error)
func CalibrationVideo(ctx context.Context, o CalibrationOptions) error
func AnalyzeCalibration(ctx context.Context, o AnalyzeOptions) (*Recommendation, error)
func Inspect(ctx context.Context, o InspectOptions) (*StreamInfo, error)
func GenerateKey() (public, private []byte, err error)

type Profile struct {
    Width, Height int        // video pixels
    Cell          int        // 4, 8, 12 or 16
    FPS           int
    Modulation    Modulation // Gray4, Gray2, RGB8, Gray8
    Protection    Protection // Fast, Normal, Robust
    ParityPercent int        // 5..100
}

type EncodeOptions struct {
    Inputs      []string
    Output      string
    Profile     Profile
    Compression Compression            // CompressAuto, CompressZstd, CompressXZ, CompressNone
    Passphrase  func() (string, error) // nil: no encryption
    SignKey     []byte                 // nil: unsigned
    DryRun      bool
    Backend     string                 // "embedded", "system" or a path
    Workers     int
    TempDir     string
    Progress    func(Event)            // called from one goroutine; nil allowed
}

type DecodeOptions struct {
    Recordings       []string
    OutputDir        string
    StateDir         string
    Passphrase       func() (string, error)
    VerifyKeys       [][]byte // non-empty: the transfer must be signed by one of these
    MaxSize          int64
    Overwrite        bool
    Report           string   // optional path for a JSON report
    Backend          string
    Workers          int
    Progress         func(Event)
}

type Event struct {
    Time        time.Time
    Stage       Stage     // StagePack, StageProtect, StageRender, StageEncodeVideo, StageDecodeVideo, StageDemodulate, StageRepair, StageVerify, StageUnpack
    Kind        EventKind // KindStarted, KindProgress, KindFinished, KindStats, KindWarning
    Done, Total int64
    Unit        Unit      // UnitBytes, UnitFrames, UnitBlocks
    Stats       *Stats    // captures, frames seen and total, clean fraction, raw bit error rate, codewords ok, blocks ready
    Warn        *Warning  // code and text: ExposureTooLong, Clipping, FramesDropped, ...
}
```

Errors are typed so a caller can act on them: not enough data (with the shortfall per block), wrong
passphrase, bad or missing signature, unsafe archive, limit exceeded, unsupported version.

Key internal interfaces. The signatures below fix the shape. The field lists of the types they name are
written in milestone M0 and may still grow, by the orchestrator's decision only, until the end of M2;
after that they are frozen.

```go
// internal/fec
type InnerCode interface {
    ID() uint8
    ShardSize() int                  // bytes
    N() int                          // sent bits
    Encode(dst []byte, shard []byte) // appends CRC, shortens, packs bits
    Decode(shard []byte, llr []int8) (iters int, ok bool)
}
func Shape(streamLen int64, shardSize int) (blocks, k int, err error) // FORMAT.md section 11
func DefaultM(k, parityPercent int) int
type Plan struct{ Blocks, K, M, ShardSize int; Packets int64; Seed uint32 } // and an unexported cache of the shard order
func NewPlan(streamLen int64, shardSize, m int, seed uint32) (Plan, error)
func (p Plan) Locate(packet int64) (block, shard int)
func (p Plan) Frames(codewordsPerFrame int) int
type ShardStore interface {
    Put(block, shard int, b []byte) error
    Get(block, shard int, b []byte) (bool, error)
    Have(block int) int
    Close() error
}

// internal/frame
func HeaderSlot(copy, i, r int) PointF // fractions of the finder rectangle; needs no grid
func PalettePatch(copy, k int) RectF   // likewise
func NewLayout(gridX, gridY int) (*Layout, error)
func (l *Layout) DataCells() []Cell
func (l *Layout) Anchors() []Anchor
func (h Header) Encode() [236]bool
func DecodeHeader(soft []int8) (Header, bool)
func NewMapping(h Header) (*Mapping, error)                 // FORMAT.md section 9
func (m *Mapping) CodewordsPerFrame() int
func (m *Mapping) Position(frame uint32, codeword, bit int) int
func (m *Mapping) ScrambleBit(frame uint32, position int) uint8

// internal/modem
type Modem interface {
    ID() uint8
    BitsPerCell() int
    Planes() PlaneMask
    Map(symbol uint8) Sample
    LLR(dst []int8, obs Obs, st *LocalStats, weight float32)
}

// internal/vision
type Detector interface { Detect(f *video.Frame, prev *Quad) (Quad, bool) }
type HeaderReader interface { Read(f *video.Frame, q Quad) ([]frame.Header, bool) } // one per distinct frame index seen
type Rectifier interface { Fit(f *video.Frame, q Quad, l *frame.Layout) (*Mesh, []AnchorObs, error) }
type Sampler interface { Sample(dst []modem.Obs, f *video.Frame, m *Mesh, cells []frame.Cell) }
type Equaliser interface { Fit(obs []modem.Obs, palette []modem.Obs, l *frame.Layout, a []AnchorObs) *Field }
type PhaseEstimator interface { Estimate(a []AnchorObs, l *frame.Layout) PhaseField }

// internal/video
type Frame struct {
    W, H      int
    Y, Cb, Cr []uint16 // 8- or 10-bit samples; Cb and Cr nil for brightness-only
    Bits      uint8
    Index     int
    PTS       time.Duration
}
type Encoder interface { WriteFrame(*Frame) error; Close() error }
type Decoder interface { Info() ClipInfo; Next(*Frame) error; Close() error } // io.EOF at the end
type Backend interface {
    Probe(ctx context.Context, path string) (ClipInfo, error)
    NewEncoder(ctx context.Context, path string, c EncodeConfig) (Encoder, error)
    NewDecoder(ctx context.Context, path string, c DecodeConfig) (Decoder, error)
}
```

What the named types carry:

| Type | Package | Carries |
| --- | --- | --- |
| `Cell`, `Anchor` | frame | Grid coordinates of a cell; of an anchor's top-left cell |
| `PointF`, `RectF` | frame | A point or rectangle as fractions of the finder-centre rectangle |
| `Header`, `Descriptor` | frame | The fields of FORMAT.md sections 6 and 10 |
| `Sample` | modem | Y', Cb, Cr code values and their bit depth |
| `PlaneMask` | modem | Which planes a modulation uses |
| `Obs` | modem | One cell's mean value per plane, and whether it is saturated |
| `LocalStats` | modem | A centre and a variance per level, for one tile |
| `Quad` | vision | Four finder centres in image coordinates, and the module size |
| `Mesh` | vision | The mapping from cell coordinates to image coordinates |
| `AnchorObs` | vision | An anchor's located corner, its black and white references, its four phase intensities |
| `Field` | vision | `LocalStats` per tile, with interpolation |
| `PhaseField` | vision | For each cell, a weight for each of the four phase values |
| `ClipInfo` | video | Codec, size, bit depth, frame rate, duration, rotation, transfer function |
| `EncodeConfig`, `DecodeConfig` | video | Size, rate, quantiser, bit depth; time slice and brightness-only for decoding |
| `StreamInfo`, reports, `Stats`, `Warning` | root | What `inspect`, `--report` and the dashboard show |

**GUI readiness** rests on three rules. The core does no terminal I/O and never exits the process. All
progress is typed events delivered to a callback. Cancellation is by `context`. A Go GUI links the root
package directly; any other GUI runs the binary with `--json` and reads events.

**Concurrency rule.** Workers share no mutable state. Each owns its engine instance and buffers, and
passes results through channels to a single goroutine that owns the accumulators and stores.

## 15. Testing

- **Test first.** Each task lands as a commit of failing tests followed by a commit that makes them pass.
- **Coverage.** At least 95% of statements in every package, enforced on each pull request. Excluded:
  `cmd/qrvt/main.go`, generated tables, `tools/` and `spikes/`. Runs of a coverage-instrumented binary
  count.
- **Golden vectors** freeze the format; see FORMAT.md section 13.
- **Fuzzing and hostile inputs**: SECURITY.md.
- **Real recordings**: `QRVT_RECORDINGS=<dir> go test -tags realdevice ./...`, on the owner's machine only.

### Simulated camera

`internal/channel` is a chain of pure functions, each with explicit parameters and a seed:

1. **Display**: real or stand-in H.264, player scaling, gamma, black level, panel response, backlight
   flicker, scan-out timing, optional sub-pixel structure.
2. **Timing**: camera frame rate, jitter, exposure, rolling readout, dropped frames, a freeze at the seam.
3. **Geometry**: pose, hand-shake random walk, rolling-shutter shear, radial distortion.
4. **Optics**: defocus and motion blur, vignetting, veiling and specular glare, interference fringes.
5. **Sensor and processing**: noise, white balance, colour matrix, tone curve, sharpening, colour noise
   reduction, clipping, 4:2:0.
6. **Recorder codec**: in integration tests, x264 through the embedded engine (the engine has no HEVC
   encoder) at the bitrates below, which are the HEVC figures of section 11 times 1.4; in unit tests, a
   pure-Go quantiser stand-in.

The presets below are starting values chosen to bracket the simulated cases of section 4. Measurements
from steps S2 and G1 replace them.

| | `ideal` | `tripod` | `handheld` | `budget-android` | `iphone-hdr` | `harsh` |
| --- | --- | --- | --- | --- | --- | --- |
| Camera | 4K60 | 4K60 | 4K60 | 1080p30 | 4K60, 10-bit HLG | 4K30 |
| Screen fills, of frame width | 0.90 | 0.90 | 0.80 | 0.80 | 0.80 | 0.70 |
| Tilt up to, degrees | 0 | 5 | 15 | 15 | 15 | 25 |
| Shake, pixels RMS per capture | 0 | 0 | 3 | 3 | 3 | 8 |
| Lens distortion k1 | 0 | 0.02 | 0.04 | 0.06 | 0.04 | 0.08 |
| Blur sigma, camera pixels | 0.5 | 1.0 | 1.5 | 1.5 | 1.5 | 2.0 |
| Noise sigma, 8-bit code values | 0 | 2 | 4 | 6 | 4 | 8 |
| Exposure / readout, ms | 1 / 0 | 4 / 8 | 8 / 8 | 16 / 25 | 8 / 8 | 16 / 25 |
| Gamma error | 0 | 0.1 | 0.2 | 0.2 | 0.2 | 0.3 |
| White-balance shift | 0 | 2% | 5% | 5% | 5% | 10% |
| Corner vignetting | 0 | 5% | 10% | 15% | 10% | 25% |
| Sharpening amount | 0 | 0.3 | 0.5 | 0.8 | 0.5 | 1.0 |
| Fringe / flicker amplitude | 0 | 0 | 2 codes / 2% | 2 codes / 2% | 2 codes / 2% | 6 codes / 6% |
| Glare patch, share of code area | 0 | 0 | 0 | 0 | 0 | 2% |
| Dropped captures | 0 | 0 | 0.5% | 1% | 0.5% | 3% |
| Recorder bitrate, Mbit/s | lossless | 74 | 74 | 17 | 74 | 32 |

| Impairment | Asserted property |
| --- | --- |
| Perspective to 25 degrees, lens distortion, rolling shear | Cell-centre error at most 0.15 cell RMS; finders found in at least 99.9% of 1,000 random poses; the header read in at least 99.5% of captures on `handheld` |
| Blur, noise, recorder quantiser | Raw bit error rate never rises as a parameter improves, and stays within 10% of reference values stored when M2 is merged |
| Gamma, white balance, tone curve, vignetting | Confidence is honest: with soft bits grouped into ten buckets by stated confidence, the observed error rate of every bucket holding at least 10,000 bits lies between half and twice the stated rate |
| Moire fringes, flicker bands | Decoding succeeds with stripe cancellation on at an amplitude where it fails with it off |
| Frame mixing, any boundary direction | Phase weight error at most 0.05; no soft bit is ever credited to the wrong code frame |
| Camera at twice the display rate, any phase offset | One pass succeeds for every offset |
| Camera equal to the display rate | At 30 fps both, 4 ms exposure and 8 ms readout: succeeds within 4 loop passes |
| Dropped frames, bursts, mid-loop start | With parity 20, whose budget is 16.7% of frames: succeeds at 14.7% random loss; at 18.7% reports which blocks are short and writes nothing |
| Glare patches, overlays | `normal` succeeds with a patch over 5% of the code; `robust` with 20% |
| Two partial recordings | Each alone fails; together they succeed |
| Whole system | SHA-256 round trip for every row of the matrix below; identical raw frame hashes on every platform |

Round-trip matrix. `--hdr` joins it in M6.

| Preset | Video | Modes | Protection |
| --- | --- | --- | --- |
| `ideal` | 480p at 12 fps | bw, gray4, color | fast, normal, robust |
| `tripod` | 1080p at 30 fps | gray4 | fast, normal |
| `tripod` | 1080p at 30 fps | color | normal |
| `handheld` | 1080p at 30 fps | bw, gray4 | normal, robust |
| `budget-android` | 720p at 12 fps | bw, gray4 | normal, robust |
| `iphone-hdr` | 1080p at 30 fps | gray4 | normal |
| `harsh` | 720p at 12 fps | bw | robust |

These thresholds and this matrix are untested estimates. A cell that fails is a defect unless the
feasibility conditions of section 11 say it should not have passed, in which case it is removed with a
note in this document.

### Test videos

| Kind | Produced by | Use |
| --- | --- | --- |
| To film | `qrvt calibrate make` | Steps through every mode, density and display rate on a real screen |
| Synthetic "as filmed" | `tools/dev testvideos` | One clip per simulator preset and one per single impairment, deterministic; a small set is committed |
| Reference | Committed once per format version | Every later release must still decode it |

## 16. Scripts

| | macOS and Linux | Windows | Does |
| --- | --- | --- | --- |
| Setup | `scripts/setup.sh` | `scripts/setup.ps1` | Installs or verifies pinned versions of Go, the linter, the release tool and git through Homebrew, apt, dnf, pacman or winget. Native ffmpeg is optional |
| Build | `scripts/build.sh` | `scripts/build.ps1` | Static build for the host, or all six targets |
| Test | `scripts/test.sh` | `scripts/test.ps1` | Unit and integration tests with the coverage gate; options for fuzzing, the simulator matrix and real recordings |

The setup scripts stand alone, because Go may not be installed yet: they install or verify Go itself and
then run `go run ./tools/dev install-tools`. With `--spike` they also install the two tools only spike
S1 needs, a native ffmpeg with an HEVC encoder and the wasmtime command-line runner. They print what they
are about to install before installing anything. The build and test scripts are thin wrappers around
`go run ./tools/dev <task>`, so that logic exists once. CI calls the same scripts on all three systems.

## 17. CI and release

- **Each pull request**: lint; tests on Linux, macOS and Windows for amd64 and arm64; race detector;
  coverage gate; vulnerability scan; static-build check for all six targets; release snapshot;
  licence-compatibility check; short fuzz runs; docs build. Builds and ordinary tests run with
  `CGO_ENABLED=0`. The race detector needs cgo on most systems and does not support windows/arm64, so
  race tests run with cgo on the other five runners.
- **Nightly**: long fuzzing, the full simulator matrix, a 1 GB loopback with memory assertions, and
  benchmarks with regression alerts.
- **Security**: CodeQL, dependency updates, actions pinned by commit, least-privilege tokens.
- **Release**: release-please keeps the changelog and version from conventional commits. A `v*` tag runs
  GoReleaser: archives, checksums, keyless cosign signature, SBOM, build provenance, Homebrew cask, Scoop
  manifest, winget pull request, deb, rpm and apk packages, macOS notarisation and Windows signing.
  Steps whose secrets are absent are skipped.
- **Docs**: a website built from `docs/` and published to GitHub Pages on each release, containing the
  user guide, this design, the format specification and an architecture guide.

GoReleaser was chosen because it covers every channel above in one configuration; the alternative is
writing each package format by hand.

## 18. Build plan in brief

| Step | Content |
| --- | --- |
| M0 | Repository, scripts, CI skeleton, frozen interfaces, the arithmetic of the format; spikes S1 (engine speed, streaming, six targets, wazero against wasmtime) and S3 (LDPC speed) |
| S2 | The owner films a test pattern on each device; the real channel is measured and the default mode confirmed or changed. Does not block M1-M3 |
| M1 | Building blocks in parallel, including keys and signing |
| M2 | Vision, including header reading and stripe cancellation |
| M3 | Pipeline, public API, merging of recordings, hostile-input suite |
| M4 | `calibrate`, `inspect`, profiles |
| G1 | The owner films the calibration video and sample transfers |
| M5 | Real-device tuning of every mode, of merging and of stripe cancellation; 10-bit recordings |
| M6 | 1 GB scale, resuming with `--state-dir`; experimental `--hdr` |
| G2 | The owner films one large transfer |
| M7 | Website, packaging, signing, release dry run |
| M8 | Final security pass, then the first release candidate |

Thresholds, fallbacks, ownership and exit tests are in IMPLEMENTATION_PROMPT.md.

## 19. Risks

| Risk | Mitigation | How it would show |
| --- | --- | --- |
| Real phone processing is worse than simulated; grey levels merge or clip | S2 before the default is frozen; pluggable modes; black-and-white fallback; clipping warning | S2 measurements; real-clip regression set |
| Embedded engine too slow, deadlocks on pipes, or fails on a target | S1 thresholds with named fallbacks; per-core slicing; native ffmpeg option | S1 benchmarks on every CI runner |
| Frame mixing worse than modelled | 2-D phase field; conservative default rate; clean-fraction readout | Simulator phase sweep; S2 clips |
| Player drops frames or processes the picture | 4-pixel cells keep bitrate low; bitrate caps per preset; `calibrate` counts frame-index gaps | Gap statistics from real playback |
| Sub-pixel registration fails under shake or stabilisation | Anchors every 30 cells; outlier rejection | Position-error assertion |
| Glare, exposure and focus hunting | Constant mean brightness; saturated cells erased; capture guidance | Glare and exposure-step simulator cases |
| Moire when display and camera pixels nearly match | Stripe cancellation; `calibrate` advises a change of distance | Sub-pixel-structure simulator case |
| Miscalibrated confidence or a wrong LDPC table | Honest-confidence test; published-threshold test | Both tests in M1 and M2 |
| 1 GB scale: 8.5 GB video, 13 GB recording, hours of processing, clip-length limits on phones | Streaming stages, disk stores, resume, merging split recordings | Nightly 1 GB run |
| One maintainer behind go-ffmpreg; GPL source obligations for the embedded binary | Version and checksum pinned; build script and licence notice in the repository | Release checklist |

## 20. Sources

Checked on 2026-11-13 by research agents; "verified" means seen at the address given.

| Topic | Source | Status |
| --- | --- | --- |
| go-ffmpreg v0.6.22, ffmpeg 5.1, GPL-3.0, x264 included, no x265 | https://codeberg.org/gruf/go-ffmpreg | Verified |
| wazero v1.12.0; compiler on amd64 and arm64 only | https://github.com/tetratelabs/wazero/releases | Verified |
| WebAssembly runtime speeds relative to native | https://00f.net/2026/06/23/webassembly-runtimes-2026/ | Verified through a search summary; one cryptographic workload |
| wasmtime support tiers | https://docs.wasmtime.dev/stability-tiers.html | Verified |
| klauspost/reedsolomon v1.14.2, Leopard GF(2^16) limits | https://pkg.go.dev/github.com/klauspost/reedsolomon | Verified |
| No soft-decision LDPC library in Go | https://github.com/xdsopl/LDPC, https://github.com/nathanhack/ecc | Verified |
| libcimbar design, throughput and lessons | https://github.com/sz3/libcimbar | Verified |
| Rolling shutter with frame mixing (FareQR) | https://hhannuaa.github.io/papers/sec_fareqr_2020.pdf | Verified |
| Pure-Go H.264 stack defects | https://github.com/oops1/go.264 | Verified in the repository |
| gen2brain/h265 v0.2.5 | https://github.com/gen2brain/h265 | Verified; speed not independently measured |
| Eyevinn/hi264 v0.12.2, mp4ff v0.62.1 | https://github.com/Eyevinn/hi264, https://github.com/Eyevinn/mp4ff | Verified |
| Charm v2 libraries | https://pkg.go.dev/charm.land/bubbletea/v2, https://pkg.go.dev/charm.land/fang/v2 | Verified |
| GoReleaser v2.20.2 and its deprecations | https://goreleaser.com/deprecations/ | Verified |
| iPhone 4K60 recording size | https://www.idownloadblog.com/?p=532811 | Third-party report |
| IEEE 802.11n LDPC matrices | IEEE Std 802.11-2020 Annex F; https://github.com/simgunz/802.11n-ldpc | Not opened; the build plan requires two sources that agree |
| Memory-safe language guidance | https://www.nsa.gov/Press-Room/Press-Releases-Statements/Press-Release-View/Article/4223298/ | Verified through secondary coverage |

## Change log

| Date | Change |
| --- | --- |
| 2026-11-13 | M0: section 14 names the enum constants with a prefix (`StagePack`, `KindStats`, `UnitBytes`, `CompressAuto`, ...) because `Stats` and `Warning` are also type names; `Plan` caches its shard order in an unexported field so that `Locate` is O(1) |
