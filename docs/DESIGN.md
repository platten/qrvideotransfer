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
| Parsing untrusted video | ffmpeg inside the wazero sandbox; no published sandbox advisories, though less scrutinised. Every codec, HEVC included, is decoded in the sandbox; a pure-Go HEVC decoder outside it is only an opt-in fast path (section 12) | wasmtime is heavily fuzzed but had several sandbox-escape CVEs in 2024-26, two rated critical in October 2026. The pure-Rust H.264 and HEVC decoders are all under 8 months old with single maintainers |
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

**Outcome (spike S1).** wasmtime ran the same module 1.22-1.30x faster than wazero on the same clips,
well under twice as fast, so work continues in Go. wazero decodes 4K HEVC at 3.1-3.9 fps, below the
4 fps target; that is accepted for version 1, with two opt-in speed-ups (section 12).

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
| F3 | The display video's size depends on aligning cells to H.264's 4x4 transform blocks | Bits per cell at quantiser 32: four greys at 4 pixels 8.4 (x264 `veryfast`; 6.1 with `medium` or the settings of section 12, spike S1); at 8 pixels 10.1; at 3 pixels 29.1; at 6 pixels 26.3; eight colours at 4 pixels 28.1. No cell errors through the display codec up to quantiser 38 |
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
  encoder. Spike S1 found its libaom unusable for 10-bit AV1: 0.33 fps at 1080p (realtime, cpu-used 8)
  and 0.07 fps (good quality, cpu-used 6, 724 MB). The measured alternative is 10-bit H.264 (High 10)
  from the engine's x264: 2.3 fps at 1080p, 1.4 fps at 4K, about 10.5 bits per cell, no cell errors;
  but many stock players and hardware decoders lack High 10. The choice is made in M6. This mode
  cannot play in every stock player and is labelled experimental.

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

- Encoder (`internal/fec/ldpc`): systematic, through the dual-diagonal parity structure; the first
  parity block is the sum of the per-row syndromes of the information part, each later one follows
  from the row before.
- Decoder: layered normalised min-sum, one layer per block row, scaling 13/16 (computed as
  floor(13m/16)), 8-bit soft bits multiplied by 4 on entry so that small messages keep their
  precision, int16 messages, at most 25 iterations (`ldpc.MaxIterations`). M1 shipped scaling 0.75
  (floor(3m/4)) on the soft bits as given; the follow-up below replaced it by measurement. The
  checks are tested before the first iteration (a clean codeword costs 0 iterations) and after each
  one, stopping when all pass; then the CRC. Block columns that are entirely shortened are left out of
  the schedule; the known-zero bits of the one partly shortened column always send the largest
  magnitude with sign 0, so the checks can never flip them. Posteriors are clamped to +-8,191 and
  check-to-variable magnitudes to 24,573, which keeps every message within int16 without saturation
  (|L - R| <= 32,764), so no input can overflow; a fuzz target checks that arbitrary soft bits never
  panic and stop within the cap.
  A `Code` holds its working memory and belongs to one worker.
- Speed (spike S3, `normal` code, one core, sent bits): 7.6 Mbit/s at a fixed 10 iterations in portable
  Go, against a threshold of 20 Mbit/s, which is **not met**; 18.6 Mbit/s with the experimental
  `simd/archsimd` package; 19.4 Mbit/s with early stopping at Eb/N0 3.0 dB (0.5 dB above the
  waterfall, mean 3.5 iterations). Decoding is parallel across codewords.
- Speed of the production decoder (M1, `BenchmarkDecode`: `normal` code, one goroutine pinned to one
  core, early stopping, 64 noisy codewords, sent bits; median of 6 runs with range): 9.8 Mbit/s
  (9.3-11.6) at Eb/N0 2.5 dB, mean 6.0 iterations; 14.2 Mbit/s (13.1-16.1) at 3.0 dB, 3.7 iterations;
  22.7 Mbit/s (19.5-23.7) at 4.0 dB, 2.25 iterations. The S3 spike decoder, rebuilt from history and
  run alternately with it on the same core, gave 8.8-10.4 and 13.8-14.1 Mbit/s at 2.5 and 3.0 dB
  against 8.2-10.8 and 13.0-16.0 for the production decoder in the same session: the same speed within
  the noise. Both are below S3's own figures (13.4 and 19.4) because the machine was shared with other
  jobs during M1; the S3 verdict (threshold not met per core) stands. No SIMD path was written.
- Speed after the decoder follow-up (`BenchmarkDecodeCandidates`, the same 64 codewords, M1 and new
  decoder in 5 alternating runs pinned to one core, median): 12.9 against 12.2 Mbit/s at Eb/N0 2.5 dB
  (5.73 against 6.03 mean iterations) and 19.7 against 18.9 Mbit/s at 3.0 dB (3.55 against 3.67): the
  new decoder is no slower, because it needs fewer iterations.
- Release builds must not depend on a Go experiment or an amd64-only package. The portable decoder is
  the default on all six targets; a SIMD path may exist only as an optional build behind a build tag,
  tested to give output identical to the portable decoder.
- The matrices must come from the standard, not from memory. A wrong table still gives a
  self-consistent code, so the guard is an error-rate test against published curves (`TestErrorRate`):
  the unshortened mother code of each rate, BPSK over AWGN, soft bits 4 per unit of LLR, must reach
  frame error rate 1e-2 within 0.5 dB of the published Eb/N0, shown with 95% confidence (at most 4
  frame errors in 1,000 frames per rate; 20,000 frames with `QRVT_LONG_TESTS=1`). Measured
  (`TestErrorRateCurves`, 4,000 frames per 0.1 dB step), Eb/N0 at FER 1e-2:

  | Rate | Published | Source | Measured (decoder of the follow-up) | Measured (M1 decoder) |
  | --- | --- | --- | --- | --- |
  | 1/2 | 1.81 dB | [A] Fig. 4, layered offset min-sum, 12 iterations | 1.51 dB | 1.82 dB |
  | 2/3 | 2.32 dB | [B], flooding sum-product, 20 iterations | 2.23 dB | 2.23 dB |
  | 3/4 | 2.84 dB | [B], flooding sum-product, 20 iterations | 2.76 dB | 2.77 dB |
  | 5/6 | 3.70 dB | [A] Fig. 4, layered offset min-sum, 12 iterations | 3.50 dB | 3.53 dB |

  [A] Balatsoukas-Stimming, Giard, Burg, "Comparison of Polar Decoders with Existing Low-Density
  Parity-Check and Turbo Decoders", arXiv:1702.04707v2, 2017, curve "IEEE 802.11n (I = 12)", BPSK,
  Eb/N0. [B] S. Tavildar, github.com/tavildar/LDPC, `results/wifi_ldpc_qpsk.png`, curves N = 1944, one
  bit per real dimension, Eb/N0; the only source found for rates 2/3 and 3/4, and not peer-reviewed.
  Each published value is a log-linear interpolation between the two bracketing points of the curve.
  Our decoder is within 0.3 dB of every reference and never behind it (it runs more iterations than
  [A] and [B]). A cross-check from Roth et al., IEEE ASSCC 2010, Fig. 5 (Z = 81, 10 iterations of a
  5-bit offset min-sum) gives 2.16 dB and 3.71 dB for rates 1/2 and 5/6.
- The shortened inner codes that ship (ids 1-4) have no published curves, so they are checked against
  the binary-input AWGN capacity limit for their rate, counted with the CRC: R = (8S + 32) / N sent
  bits (`TestShortenedErrorRate`, the weaker check). Measured (BPSK, AWGN, same channel as above):

  | Code | R | Capacity limit | FER 1e-2 at Eb/N0 | Gap | Es/N0 at FER 1e-2 | M1 decoder: Eb/N0, gap |
  | --- | --- | --- | --- | --- | --- | --- |
  | 1 `fast` | 0.829 | 2.32 dB | 3.44 dB | 1.12 dB | 2.63 dB | 3.46, 1.14 dB |
  | 2 `normal` | 0.685 | 1.17 dB | 2.49 dB | 1.32 dB | 0.85 dB | 2.48, 1.31 dB |
  | 3 | 0.620 | 0.79 dB | 2.10 dB | 1.31 dB | 0.02 dB | 2.07, 1.28 dB |
  | 4 `robust` | 0.359 | -0.40 dB | 1.35 dB | 1.75 dB | -3.10 dB | 2.20, 2.60 dB |

  Es/N0 = Eb/N0 + 10 log10(R) is per sent bit, which is what the noise of a cell sets. The test
  asserts FER below 1e-2 at the limit plus 2.5 dB for all four codes. With the M1 decoder `robust`
  missed that criterion by about 0.1 dB, and the cause was the decoder, not the code; with the decoder
  of the follow-up it meets it with 0.75 dB to spare, and a regression check holds it at the limit
  plus 2.1 dB (the measured 1.75 dB plus 0.3 dB of margin, 5,000 frames because its waterfall is
  shallow). Codes 1-3 moved by at most 0.03 dB, within the resolution of these sweeps.
- Decoder comparison (`TestMinSumVariants`, `QRVT_LONG_TESTS=1`; a test-only floating-point reference
  with the same layered schedule; 4,000 frames per 0.1 dB step, the same frames for every decoder; Eb/N0
  at FER 1e-2, each code's capacity gap in brackets):

  | Decoder | `robust` (4) | code 3 | `normal` (2) | `fast` (1) |
  | --- | --- | --- | --- | --- |
  | M1 decoder: int16, x0.75 as floor(3m/4), 25 iterations | 2.26 (2.66) | 2.07 (1.29) | 2.49 (1.32) | 3.48 (1.16) |
  | Sum-product, 50 iterations, float LLRs | 1.13 (1.53) | 1.97 (1.18) | 2.43 (1.25) | 3.41 (1.10) |
  | Sum-product, 50 iterations, the shipped 8-bit soft bits | 1.14 | 1.98 | 2.42 | 3.42 |
  | Min-sum x0.75, float, 25 iterations | 1.96 | 2.07 | 2.49 | 3.46 |
  | Min-sum x0.75, float, 50 iterations | 1.93 | 2.04 | 2.46 | 3.45 |
  | Min-sum x0.8125, float, 25 iterations | 1.36 | 2.12 | 2.51 | 3.45 |
  | Min-sum x0.875, float, 25 iterations | 1.35 | 2.21 | 2.60 | 3.48 |
  | Offset min-sum, 0.25 LLR, float, 25 iterations | 1.33 | 2.20 | 2.62 | 3.50 |
  | Offset min-sum, 0.5 LLR, float, 25 iterations | 1.33 | 2.07 | 2.51 | 3.47 |

  For codes 1-3 the M1 decoder is within 0.1 dB of sum-product, and x0.75 is the best of the
  min-sum variants. For `robust` it is 1.13 dB behind sum-product. About 0.3 dB of that is integer
  truncation: floor(3m/4) of small messages at the low Es/N0 where `robust` works (the float model of
  the same algorithm is at 1.96 dB, and soft bits at 8 or 16 per unit of LLR instead of 4 bring the M1
  decoder to 2.09 and 2.02 dB, `TestSoftBitUnits`). About 0.6 dB is the normalisation: after
  shortening, `robust`'s checks keep only 5 to 7 unshortened variables, and min-sum over few inputs
  needs less damping than x0.75. The 8-bit soft bits cost nothing (sum-product on them equals
  sum-product on float LLRs).
- Decoder follow-up (after M1, designer's decision; no format bit changes). Two integer candidates
  in shipping form were measured on the same frames (`TestDecoderCandidates`, `QRVT_LONG_TESTS=1`,
  20,000 frames per 0.1 dB step, sum-product on the first 4,000), with the soft bits handed to the
  decoder multiplied by 0.5, 1 and 2 relative to their true LLRs before quantisation and clipping
  (that is 2, 4 and 8 steps per unit of LLR). Eb/N0 in dB at FER 1e-2 / 1e-3:

  | Decoder | Scale | `robust` | code 3 | `normal` | `fast` |
  | --- | --- | --- | --- | --- | --- |
  | M1: x0.75 | 0.5 | 2.60 / 3.05 | 2.13 / 2.34 | 2.50 / 2.71 | 3.51 / 3.72 |
  | | 1 | 2.28 / 2.80 | 2.08 / 2.26 | 2.47 / 2.68 | 3.48 / 3.71 |
  | | 2 | 2.11 / 2.64 | 2.07 / 2.26 | 2.46 / 2.66 | 3.47 / 3.71 |
  | A1: offset 1 step | 0.5 | 1.37 / 1.60 | 2.09 / 2.29 | 2.49 / 2.72 | 3.49 / 3.72 |
  | | 1 | 1.34 / 1.60 | 2.20 / 2.40 | 2.58 / 2.81 | 3.53 / 3.75 |
  | | 2 | 1.50 / 1.76 | 2.34 / 2.54 | 2.70 / 2.92 | 3.58 / 3.79 |
  | A2: offset 2 steps (0.5 LLR) | 0.5 | 2.20 / 2.38 | 2.28 / 2.42 | 2.63 / 2.82 | 3.58 / 3.77 |
  | | 1 | 1.34 / 1.61 | 2.07 / 2.26 | 2.48 / 2.71 | 3.48 / 3.71 |
  | | 2 | 1.34 / 1.62 | 2.19 / 2.38 | 2.58 / 2.81 | 3.52 / 3.75 |
  | A3: offset 3 steps | 0.5 | 3.16 / 3.32 | 2.76 / 2.88 | 3.03 / 3.19 | 3.84 / 3.99 |
  | | 1 | 1.68 / 1.90 | 2.11 / 2.29 | 2.50 / 2.71 | 3.49 / 3.70 |
  | | 2 | 1.29 / 1.54 | 2.11 / 2.31 | 2.51 / 2.75 | 3.49 / 3.73 |
  | **B: x13/16, soft bits x4 inside** | 0.5 | 1.47 / 1.89 | 2.08 / 2.27 | 2.47 / 2.68 | 3.47 / 3.69 |
  | | 1 | 1.41 / 1.82 | 2.09 / 2.28 | 2.48 / 2.67 | 3.46 / 3.69 |
  | | 2 | 1.39 / 1.77 | 2.09 / 2.27 | 2.48 / 2.70 | 3.46 / 3.67 |
  | Sum-product, float, 50 iterations | 0.5 | 1.72 / 1.84 | 2.60 / 2.71 | 2.95 / 3.05 | 3.82 / 3.94 |
  | | 1 | 1.13 / 1.41 | 1.96 / 2.13 | 2.37 / 2.61 | 3.43 / 3.68 |
  | | 2 | 1.42 / 1.66 | 2.25 / 2.43 | 2.61 / 2.76 | 3.53 / 3.77 |

  A2 with the soft bits multiplied by 4 inside (offset 8) gives exactly A2's results: offset min-sum
  scales exactly, so up-scaling buys it nothing, and integer A2 at scale 1 (1.34 dB) equals its float
  model (1.33 dB). Frames that converged to a wrong codeword (every check satisfied, information wrong;
  CRC-32C catches them), summed over the sweep: none for any decoder on codes 2 and 3; on `robust` 1
  for A1 (scale 1), A2 and A3 (scale 2 each), none for B, M1 and sum-product; on `fast`, at high error
  rates, 5-7 per scale for every min-sum decoder including M1 and B (0 for A3 at scale 0.5), and 0-1 for sum-product.

  **Choice: B, one factor for all codes.** At scale 1 it keeps codes 1-3 within 0.02 dB of M1 at FER
  1e-2 and is no slower (above). **It does not meet the hard requirement "no worse than M1 at FER
  1e-3" strictly.** A paired comparison (`TestPairedM1B`, the same 400,000 frames through both
  decoders, McNemar's z over the frames only one of them gets wrong, positive meaning B worse) gives:

  | Code | Eb/N0 | M1 FER | B FER | Only M1 wrong | Only B wrong | z |
  | --- | --- | --- | --- | --- | --- | --- |
  | `robust` | 1.7 dB | 8.62e-2 | 1.42e-3 | 33,925 | 5 | -184.1 |
  | `robust` | 1.9 dB | 4.10e-2 | 4.38e-4 | 16,236 | 0 | -127.4 |
  | 3 | 2.2 dB | 2.39e-3 | 2.71e-3 | 283 | 411 | +4.9 |
  | 3 | 2.3 dB | 7.60e-4 | 7.72e-4 | 113 | 118 | +0.3 |
  | `normal` | 2.6 dB | 2.43e-3 | 2.79e-3 | 196 | 338 | +6.1 |
  | `normal` | 2.7 dB | 7.10e-4 | 7.88e-4 | 67 | 98 | +2.4 |
  | `fast` | 3.6 dB | 2.85e-3 | 2.22e-3 | 324 | 75 | -12.5 |
  | `fast` | 3.7 dB | 9.45e-4 | 6.75e-4 | 137 | 29 | -8.4 |

  Near FER 1e-3, B is slightly but significantly worse than M1 on code 3 and on `normal`, and
  clearly better on `fast` and on `robust`. The size of the loss, read from the local slope of each
  curve (FER falls by a factor of 3.1 per 0.1 dB on code 3 and 3.4 on `normal` here), is 0.011 dB on
  code 3 at 2.2 dB (0.001 dB, not significant, at 2.3 dB), 0.011 dB on `normal` at 2.6 dB and 0.008 dB at 2.7 dB. **The designer accepted
  this trade with an explicit tolerance: at FER 1e-3 the decoder must be within 0.02 dB of the
  previous decoder on every code**, which B meets, in return for 0.87 dB on `robust` at scale 1. It has the smallest worst case over the
  three scales (1.47 dB, against 1.50 for A1, which also fails code 2 by 0.11 dB, 2.20 for A2 and 3.16
  for A3): offset min-sum depends on the soft-bit scale, normalised min-sum does not. `robust` buys its
  3.1 dB of Es/N0 over code 3 with 56% of the payload per sent bit.
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
side. The file (`rs.Store`) adds 8 bytes per shard (a marker and a CRC-32C, so a write torn by a crash
reads back as absent), is created sparse (on Windows marked sparse with `FSCTL_SET_SPARSE` first, since
NTFS would otherwise allocate the whole size; on FAT32 it occupies its full size at once), and is
refused for a plan that holds a stream longer than 64 GiB (`rs.MaxStreamSize`, SECURITY.md section 5).
`rs.StoreSize` gives its size for the caller's free-space check. qrvt's own memory, not counting engine instances, is targeted at 600 MB at 1080p and 1.5 GB at 4K.
Each engine instance adds its linear memory, which spike S1 measured at 155 MB (8-bit) and 252 MB
(10-bit) when decoding 4K HEVC and 140-240 MB when encoding 4K, within a reserved limit of 512 MiB per
instance. Process memory for one 4K decoding instance was 319-436 MB in 16 of the 21 runs where it
was recorded, and 685-920 MB in the other 5 (920 MB when the whole reservation became resident). The default worker count is the CPU count, reduced so that the workers, each counted at the
worst measured engine figure, 920 MB, stay within half of the machine's physical memory. Each
decoding instance runs in its own worker process (section 12), which the operating system schedules,
so the worker count is not capped below the CPU count: a decoding worker spends about 300 ms of engine
time per 4K frame, against a few milliseconds of qrvt's own work on the same frame in the parent
(the frame's copy through the pipe, 10-37 ms at 4K, overlaps the next frame's decoding), so one
worker per core keeps every core busy, and vision on the same cores only fills the gaps. A worker's
peak resident memory measured 190 MB (1080p H.264), 310 MB (4K HEVC 8-bit) and 403 MB (4K HEVC
10-bit), on one machine (the S1 development machine, spike S1 section 13), not on the CI runners;
920 MB per worker stays the conservative count, because the whole 512 MiB reservation can
become resident (spike S1). On a machine with 16 GB and 16 cores that allows 8 workers. Instances in
qrvt's own process (encoding, `Concat`, and every instance when no worker process can be started) are capped
at `video.EngineInstances()`, one fewer than GOMAXPROCS (at least one). The cap is a real defence:
uncapped, stops were up to 8 times late on a CI runner (section 12). `internal/video` enforces it
itself; further instances wait their turn, with no limit but the context, so on that fallback path a
worker must not hold more decoders open than the cap without reading them. The opt-in
HEVC fast path of section 12 is not counted: its memory is not bounded against a crafted stream, which
is why it is opt-in and only for recordings the user made themselves.

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
   that bit 1, scaled by the variance, as a clipped 8-bit value in units of a quarter nat
   (`modem.LLRScale`). Saturated cells give zero.
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
- Decoding with the embedded engine, the default for every codec, is expected to take about 2.8 to 3.6
  times the recording's duration for 4K60 HEVC on 8 cores (spike S1: 21.8 fps for 8-bit and 16.8 fps
  for 10-bit with 8 instances on a 16-thread laptop), and about half that for 4K30. That is roughly 25
  to 40% slower per core than the opt-in HEVC fast path (spike S1, same session: 26% slower for 8-bit,
  37% for 10-bit). The two speed-ups, both outside the sandbox and both for recordings the user made
  themselves, are `--ffmpeg system`, which should bring decoding near real time, and the HEVC
  fast-path flag `--unsandboxed-hevc` of section 12.

## 12. Video engine

`codeberg.org/gruf/go-ffmpreg` packages ffmpeg 5.1 with x264 as a WebAssembly module, run by `wazero`.

- **Writing.** x264, intra-only, CABAC, quantiser about 32, BT.709 limited range, with `-preset
  veryfast` and `no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:no-8x8dct=1` (spike S1: 8.7 fps
  at 1080p and 1.7 fps at 4K per instance, 6.1 bits per cell, no cell errors; `medium` gives 2.4-3.0
  and 0.63 fps). Without the 8x8 transform the stream is Main profile. Workers each encode a run of
  frames to an elementary stream; all-intra streams with identical parameter sets concatenate validly,
  and one final copy pass wraps them in MP4. Each run has an even number of frames, because x264
  alternates `idr_pic_id` between 0 and 1 and consecutive IDR pictures must differ in it. In
  `internal/video` an encoder writes an MP4 (`-use_editlist 0`) or, for a run, an H.264 elementary
  stream (`.h264`) whose `Close` refuses an odd frame count (`ErrOddRun`); `Concat` wraps the runs
  into the MP4 with `-c copy`. The caller builds the `yuv420p` planes; input and output are tagged
  BT.709 limited range, so ffmpeg converts nothing. `--hdr` (M6) can use 10-bit input, encoded as
  High 10.
- **Reading.** Each worker decodes its own time slice to raw frames, without auto-rotation, brightness
  only for grey modes. Captures identify themselves, so slices overlap freely and order does not matter.
  Every codec, HEVC included, is decoded by the embedded engine inside the sandbox. That is the
  owner's decision for version 1.
  - *How a slice is read* (M1). Decoding starts 1 s before the slice (`-noaccurate_seek -ss`, with
    `-copyts`) and frames before it are dropped by qrvt: ffmpeg seeks to the random access point whose
    decode time precedes the target, and at an open-GOP point (an HEVC CRA picture) the leading
    pictures before it in display order cannot be decoded, which in the HEVC test clip lost frames
    after the target. Each frame's timestamp is read from the `showinfo` filter's log line
    (`checksum=0`) in the stream's time base, so slices that partition the duration decode every frame
    exactly once, and decoding stops at the slice's end. `-fps_mode passthrough`: ffmpeg's default
    for raw output would duplicate frames to fill gaps in the timestamps. Output is in the source's
    own pixel format (`yuv420p`, `yuvj420p`, `yuv420p10le`) or its Y plane alone (`gray`,
    `gray10le`, through `extractplanes=y`), so ffmpeg converts neither range nor depth; other formats
    are read as brightness only, 10-bit output needs a 10-bit source, and more than 10 bits is
    refused. The picture may be at most 8K UHD in either orientation (longer side 7,680, shorter
    side 4,320). Every frame must keep the probed size; a change mid-stream stops the decode. A
    `showinfo` line is recognised only at the start of a line (`[Parsed_showinfo_N @ address] n:`),
    which keeps out container metadata (printed indented) but not every text from the file: an
    unknown Matroska CodecID is logged from the start of a line and may hold a newline. So frame
    lines must also number 0, 1, 2 ... after each configuration line, and one out of sequence fails
    the decode; more timestamp lines than frames, at the end of the file or of a slice, is refused. Frames are read straight into the caller's planes through a 256 KiB buffer,
    with no frame-sized buffer of the decoder's own. An odd width or height is refused from the
    probe, since 4:2:0 chroma would be mis-framed.
  - *Engine speed at 4K HEVC* (spike S1, one instance, luma only, medians of five runs): 8-bit 3.9 fps,
    or 4.5 with `-skip_loop_filter all`; 10-bit 3.6 fps, or 3.35 with it. The 4 fps target is not met
    without skipping the loop filter; with it, it is met for 8-bit only, narrowly (3.8-4.7). Skipping
    the loop filter stays off by default until the S2 or G1 recordings show it costs no cells.
  - *HEVC fast path, opt-in.* With the HEVC fast-path flag `--unsandboxed-hevc`, HEVC is decoded instead by
    `github.com/gen2brain/h265` (v0.2.5) in the qrvt process, outside the sandbox: 4.7 fps (8-bit) and
    5.0 fps (10-bit) per core at 4K without its assembly, with luma byte-identical to the engine's. It
    is in the same trust class as `--ffmpeg system`; the help text, section 13 and SECURITY.md say that
    it is for recordings the user made themselves, that it runs outside the sandbox, and that its
    memory is not bounded against a crafted stream: the decoder never checks a slice's reference
    picture set against `sps_max_dec_pic_buffering_minus1`, and for every reference the set names that
    is missing, `generateUnavailable` (`hevc/dpb.go`) allocates a full-size picture, up to 16 + 16
    short-term and 32 long-term, about 6.5 GB at 7680 x 4320 or 1.6 GB at 3840 x 2160 (10 bits).
  - *Protections of the fast path.* They reduce risk; they do not establish a bound. For version 1
    there is no patch, no vendored copy and no slice-header parser.
    - Exactly `Threads(1)`. With any other value, including 0, which means GOMAXPROCS, the decoder runs
      wavefront rows and loop-filter bands on goroutines it starts itself, whose panics no caller can
      recover. Parallelism comes only from qrvt's own time slices, each inside its own `recover`
      boundary.
    - Before decoding, every sequence parameter set is checked, and the slice goes to the engine unless
      the stream is 4:2:0 (`hevc.SPSFormat`) at 8 or 10 bits, at most 7680 x 4320 luma samples (also
      set as `FrameSizeLimit`), and `sps_max_dec_pic_buffering_minus1[i] + 1 <= 6` for every sub-layer
      i, parsed by qrvt (the decoder keeps only the last sub-layer's value). Six is the HEVC maximum
      (Annex A.4.2, `maxDpbPicBuf`) for any picture larger than three quarters of its level's
      `MaxLumaPs`, which covers phone recordings: 3840 x 2160 at level 5.1 (8,294,400 > 6,684,672
      samples), 7680 x 4320 at level 6.2 (33,177,600 > 26,738,688) and 1920 x 1080 at level 4.1
      (2,073,600 > 1,671,168). The x265 test clips have one sub-layer with
      `sps_max_dec_pic_buffering_minus1[0] = 4`, a buffer of 5. A change of picture size, bit depth or
      chroma format starts a new decoder.
    - The engine's `-c copy` of the slice to an Annex B stream runs before decoding, as its own engine
      instance writing to the temporary directory, and finishes before the decoder starts, so a worker
      never holds both; its memory counts as an engine instance.
    - 10 s per picture. The decoder cannot be cancelled, so at the first picture that exceeds it the
      whole decode stops with an error naming the limit; no abandoned decoder is left running while work
      continues.
    - On any error, panic or refused stream the rest of the slice is decoded by the engine (from just
      after the last frame returned). CI decodes the test clips with both and requires identical
      output (`TestFastPathMatchesEngine`, whole and in slices, 8 and 10 bits).
    - *As built in M1.* The fast path is compiled only with the build tag `unsandboxedhevc` (tests:
      `-tags unsandboxedhevc,noasm`); other builds, releases included, refuse `--unsandboxed-hevc`
      (`video.ErrUnsupported`). The tag goes once the assembly question below is settled. A
      parameter set that declares a size, chroma format or bit depth other than the probed one sends
      the slice to the engine rather than starting a new decoder. The copy instance also writes each
      packet's timestamps (`framemd5`), and each picture is tagged with its access unit's index, so
      the fast path's timestamps equal the engine's. The time limit runs from a picture's first slice
      on a single decoder goroutine; at the limit that goroutine is abandoned and the decode fails.
      The copy instance's temporary directory may hold at most twice the input's size plus 64 MiB
      (`video.FastPathCopySize`, checked once a second). When the engine takes over, the old decoder
      is abandoned without a flush. A CI step runs the package's tests with
      `-tags unsandboxedhevc,noasm,nounsafe` on all six runners, and `tools/dev fuzz` adds the tag, so
      `FuzzFastPathNAL` is fuzzed.
    - The decoder's assembly is excluded, and the flag does not ship until it can be. Its `noasm` build
      tag is global: `klauspost/reedsolomon` and
      `klauspost/compress` also honour it (and `klauspost/cpuid`, which then reports no CPU features).
      M1 measures the whole release built with `-tags noasm` and accepts it only if Reed-Solomon and
      zstd each stay above about 50 MB/s per core; otherwise the fast path needs a package-specific
      tag upstream. This concerns the fast path and those libraries only, not the default path's
      safety.
    - Measured in M1 (stream B; one goroutine pinned to one core of a Core Ultra 9 285H under WSL2,
      shared with other jobs, so single runs varied by up to 4x; median of 5 runs interleaved across
      tag sets, MB/s of uncompressed data or of data shards; Reed-Solomon over GF(2^16), 128-byte
      shards; zstd from `klauspost/compress` v1.20.4 on 16 MiB of Go source and 16 MiB of random
      bytes):

      | Operation | Default | `noasm` | `nounsafe` | `noasm,nounsafe` |
      | --- | --- | --- | --- | --- |
      | RS encode, K 32,768, M 6,554 | 2,138 | 129 | 2,204 | 138 |
      | RS encode, K 100, M 20 | 5,943 | 610 | 6,148 | 663 |
      | RS reconstruct, K 32,768, M data shards lost | 300 | 45 | 354 | 49 |
      | RS reconstruct, K 32,768, M/10 data shards lost | 340 | 57 | 372 | 53 |
      | RS reconstruct, K 100, M 20 lost | 20 | 20 | 26 | 20 |
      | zstd decompress, text (default level) | 1,504 | 858 | 1,744 | 861 |
      | zstd decompress, text (`SpeedBestCompression`) | 1,742 | 1,002 | 1,995 | 1,122 |
      | zstd decompress, random | 4,217 | 4,301 | 5,232 | 4,947 |
      | zstd compress, text, default level | 341 | 355 | 374 | 301 |
      | zstd compress, text, `SpeedBestCompression` | 34 | 35 | 38 | 37 |
      | zstd compress, random, default / best | 1,931 / 338 | 2,117 / 291 | 1,966 / 310 | 1,994 / 293 |

      Decision: **the whole release can be built with `-tags noasm,nounsafe`**. That removes the
      assembly and `unsafe` of `klauspost/reedsolomon`, `klauspost/compress` (which comes in through
      `internal/payload`) and `klauspost/cpuid`, and nothing else: wazero (the sandbox itself),
      `golang.org/x/sys` and go-ffmpreg keep their `unsafe` and assembly whatever the tags, so the
      claim is only that those three libraries run no assembly or `unsafe` on decoded data. On the
      decode side zstd stays far above the bar (860 MB/s and more). Reed-Solomon reconstruction of a
      full block with `noasm` measured **45-49 MB/s per core in the worst case** (a block that has lost
      all the M data shards it can; 53-57 MB/s at a tenth of that), **below the stated bar of about
      50 MB/s**. The tags are accepted anyway because Reed-Solomon is never the bottleneck: at 45 MB/s
      one core reconstructs 30-50 times faster than one core decodes LDPC (10-14 Mbit/s of sent bits,
      about 1 MB/s of shards at the `normal` rate), and reconstructing a 1 GB transfer (about 1.2 GB of
      shards at 20% parity) takes about 27 s on one core, a few seconds over the workers, against the
      hours of video decoding the same transfer needs (section 11: a 32-minute 4K60 recording decoded
      at 2.8 to 3.6 times its duration). The cost is on the encode side: Reed-Solomon encoding falls from about 2 GB/s to about
      130 MB/s per core. `nounsafe` costs nothing measurable. Two numbers do not depend on the tags: reconstruction costs at least about 0.5-1 ms per block
      whatever its size (part of the work is a transform over the whole GF(2^16) field, so a block of
      100 shards runs at about 20 MB/s; a stream has blocks of fewer than 16,385 shards only when it is
      a single block of under 2-6 MB, so this is about a millisecond per stream), and zstd at `SpeedBestCompression` compresses text at about 35 MB/s per core with or
      without assembly, which is the encoder's own input and outside this question. Stream G sets the
      release build's tags; until it does, the default path keeps the libraries' assembly.
- **Backends.** One interface with three implementations: embedded (default), an external `ffmpeg`
  process for `--ffmpeg system`, and a raw-frame file backend for tests (`video.New`,
  `NewEmbedded`, `NewSystem`, `NewRaw`). The native backend runs the same arguments, with paths given
  as `file:` URLs, and takes `ffprobe` from beside the chosen `ffmpeg`, else from `PATH`.
- **Start-up.** The compiled module is cached on disk, so only the first run pays for compilation
  (spike S1: 7-9 s first, under 3 s cached).
- **Loading.** `go-ffmpreg/embed` decompresses the module in its `init`, costing 0.25 s and about 70 MB
  at every start, even for `--help`. `internal/video` loads the compressed module itself, on first use,
  and passes it to `go-ffmpreg/wasm`: it embeds a byte-for-byte copy of go-ffmpreg v0.6.22's
  `embed/ffmpreg.wasm.gz` (7.8 MB), checks the SHA-256 of the decompressed module
  (`357dd97d…bc766`, `video.EngineSHA256`) before compiling it, and keeps one compiled module per
  process. A test compares the copy with the module cache's file. The compilation cache is
  `qrvt/wazero` under `os.UserCacheDir()`.
- **Memory.** Each instance's linear memory is reserved at its limit, 512 MiB for decoding and
  encoding, which is also its ceiling (SECURITY.md section 5): `WithMemoryLimitPages(8192)` with
  `WithMemoryCapacityFromMax`, never grown by reallocation, since otherwise the discarded copies double
  the resident memory (spike S1). A 1 GiB reservation raised the worst-case process memory from 920 MB
  to 1,434 MB, so the limit stays at 512 MiB.
- **Sandbox.** When decoding, the module sees only the input file, read-only, through an `fs.FS`
  wrapper mounted with `FSConfig.WithFSMount` that exposes that one file and counts its reads. When
  encoding it sees one output directory. The instance that copies an HEVC stream out of its container
  for the fast path sees the input the same way, read-only, and one temporary directory, writable, where
  its output file goes; nothing else. It has no network and a memory ceiling. Every job that reads a
  recording passes `-protocol_whitelist file,pipe` and
  `-format_whitelist mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,mpegts,h264,hevc`, ffmpeg 5.1's names for
  the demuxers of MP4 and MOV, Matroska and WebM, MPEG-TS and the H.264 and HEVC elementary streams
  (a test checks them against the engine's list); the native backend gets the same. ffmpeg detects an
  HLS playlist whatever the extension, and a 110-byte live playlist named `.mp4` held ffprobe for more
  than 8 minutes, rereading it every 29 s with each read counting as progress. A probe is also bounded
  at 15 s of running time and 4 x the file's size + 16 MiB of reads (SECURITY.md section 5); probes of
  the S1 and S2 files took at most 1.2 s and read at most 1.07 times the file.
- **Watchdog.** qrvt creates the wazero runtime and passes it to go-ffmpreg, with
  `RuntimeConfig.WithCloseOnContextDone(true)`: without it, cancelling a context cannot interrupt
  running Wasm. An instance that makes no progress for 30 s is cancelled and the decode stops with an
  error naming the limit (SECURITY.md section 5, `EngineStallTimeout`). Progress is a complete output
  frame with its `showinfo` timestamp when decoding, counted by the reader; input read through the counting `fs.FS` view when probing, so one mechanism gives both
  the narrow view and the progress signal; and growth of the output file when copying. The watchdog
  bounds stalls, not total time: `MaxDecodedPixelsPerInputByte` (10,000) refuses a file whose probe
  promises more decoded pixels per input byte than that, before decoding starts, and stops a decode
  that exceeds it later in case the probe was lied to. Spike S1 measured at most 75.5 pixels per byte
  on its clips and the S2 pattern files. The option adds periodic checks to the
  compiled code, so M1 stream D re-runs the S1 decode and encode figures with it on and records both;
  a cost above about 10% goes to the owner. Measured (spike S1 report, section 10): **75-91%**, far
  above that: decoding 1080p H.264 22.3 -> 5.6 fps, 4K HEVC 8-bit 3.08 -> 0.28 fps, 10-bit
  3.02 -> 0.29 fps; encoding 1080p 7.7 -> 0.87 fps, 4K 2.1 -> 0.22 fps (one instance, medians of
  three). Reported to the owner, who chose **worker processes** instead; the option is now used only
  when no worker can be started (below).
  wazero v1.12.0 implements the option by calling a check through a trampoline at every loop
  back-edge (`internal/engine/wazevo/frontend/lower.go`, `OpcodeLoop` with `ensureTermination`),
  so the cost depends on the work: a crafted stream whose decoding is mostly picture-filling loops ran
  13 times slower (1.1-1.3 s off, 15-17 s on). The watchdog
  ticks once a second, and while an instance waits on qrvt (a full output pipe, an empty input pipe,
  its turn, the module's first compilation) it is not stalled, unless qrvt is itself waiting on it for
  a frame's timestamp; that wait also ends at the stall limit or at cancellation, and cancelling a
  decode closes its pipe (`context.AfterFunc`), so an instance blocked writing a frame stops too.
  Encoding and `Concat` instances are not stopped by the watchdog (below). At most GOMAXPROCS - 1 instances run at once
  (`video.EngineInstances()`, at least one), and an instance waiting for its turn is not stalled. The
  cap was added after a 30 s stall on the 4-vCPU CI runners stopped after 79 s, reasoning that compiled
  Wasm cannot be preempted. That reasoning does not hold while close-on-cancel is on: wazero then
  returns to Go at every loop back-edge for its check (done in Go so that it can be preempted,
  `call_engine.go`, `ExitCodeCheckModuleExitCode`), and without the cap the watchdog still stops
  every instance, but not promptly: uncapped, stops were up to 8 times late on a CI runner
  (macos-26-intel, GOMAXPROCS + 1 spinning instances, 16.8 s against a 2 s limit), while the capped
  run stayed within its bound; on a 16-thread development machine GOMAXPROCS + 2 uncapped instances
  were stopped on time (`TestWatchdogUnderLoad` asserts the capped case and logs the uncapped one).
  The cap stays as a real defence. Its hazard: waiting for a turn has no limit but the
  context, so a caller holding that many decoders open without reading them waits for ever on the next
  (`TestHoldAndWait`). This cap applies to instances in qrvt's own process (section 8).
- **Worker processes.** Every decoding and probing instance (and the fast path's copy instance) runs
  in a child process: qrvt re-executes its own binary with the reserved first argument
  `__qrvt-engine-worker`, which the root package's `EngineWorker` hook recognises first thing in main,
  before any flag parsing, so help, completion and the man page never see it. The worker's runtime
  has close-on-cancel **off**, so it runs at full speed; the parent's watchdog and cancellation kill
  the process and wait for it. The job (program, arguments, input path, output directory, cache
  directory, an output bound) travels over the worker's standard input as one length-prefixed JSON
  message, never in its arguments or environment, and the worker validates it as untrusted input. The
  job carries the protocol's version; a worker refuses any other (the executable on disk may have been
  replaced by another release while qrvt runs). The worker sends its ready message before any guest
  code runs, so the parent treats anything other than that message as the worker's first (that
  refusal, any other exit, a broken or early message, the worker ending) as a failure to start,
  passes on nothing the worker sent, and runs the job in process (*Fallback*, below). The
  program's standard input, if any, is sent only once the worker reports the engine loaded. The
  worker keeps every control: the read-only single-file counting view, the 512 MiB ceiling, and
  `MaxDecodedPixelsPerInputByte` as a bound on its output. Results come back as typed, length-prefixed
  messages (output, log, input reads, exit status) that the parent bounds before allocating and
  sanitises before printing; a lying worker can end a job, not make the parent allocate. The worker
  gets an empty environment (on Windows only `SYSTEMROOT`, `TEMP`, `TMP`), its working directory in the
  temporary directory, and no descriptors or handles but its three standard ones: on Unix it closes,
  as it starts, every descriptor above 2 without close-on-exec (any one qrvt itself inherited, which
  exec passes on; Go opens its own with the flag), and on Windows Go passes a child only the handles
  it names. It exits by itself the moment its
  standard input closes, which happens when the parent exits or dies, and runs with GOMAXPROCS of at
  least 2 so that the goroutine watching the pipe can run while the engine occupies a core; on Windows
  it is also placed in a kill-on-close Job Object: it is created suspended and resumed only once it is
  in the job, so it never runs outside it (if it cannot be assigned it runs without one). That exit is best effort: with close-on-cancel off
  a spinning guest never returns to Go, and it also blocks Go's stop-the-world garbage collection, so
  while it spins the worker's own timers and goroutines cannot be trusted. The parent's kill is the
  control. Loading and compiling the engine, which on a first run
  of a slow machine takes longer than 30 s, is not a stall: the worker sends a ready message once the
  engine is loaded (before any guest code runs), the watchdog counts from then on, and the start and
  loading phase is bounded by `video.EngineLoadLimit`, 10 minutes, after which the worker is killed.
  Compiling in qrvt's own process is not a stall either and has no limit but the context. Measured on
  one machine (the S1 development machine, spike S1 section 13), not on the CI runners, against the
  engine in process with the option off: 1080p H.264 decoding 9% slower (most of it the 0.42-0.50 s
  start-up on a 13 s run), 4K HEVC 8-bit 2% faster and 10-bit 6% slower, all within 10%; a second
  run after the PR #9 merge gave 5%, 2% and 4% slower, with a start-up of 0.27-0.29 s.
  - *Encoding and `Concat` stay in process* with close-on-cancel off: through a worker encoding cost
    11-15% (above the 5% bar), and their input is qrvt's own frames and runs, not a file from outside.
    They have no forced stop: neither the watchdog nor cancellation can interrupt their guest.
    Cancelling closes their input (`context.AfterFunc`), which stops them at their next read, within a
    frame. While their guest computes it delays qrvt's own stop-the-world garbage collection (up to
    about 0.5 s for a 4K frame), and a guest that spun would stall it, and with it the whole process;
    that matters to a host that encodes and decodes in one process (SECURITY.md T1).
  - *Fallback.* When the host never called the hook, its executable cannot be found or a worker
    cannot be started or fails before its ready message, the instance runs in qrvt's process with close-on-cancel on, under the
    `EngineInstances()` cap: the same guarantees, 4 to 11 times slower. Two calls report it.
    `(*video.Embedded).InProcess` is per backend: it returns the first reason any job of that backend
    ran in process, and keeps returning it. The pipeline makes one backend per decode, asks it at the
    decode's end, and warns once for that job (`WarnEngineInProcess`). `video.WorkerStatus` is the
    process's current state: it returns the reason while no worker can be used, or after a failed
    start until a worker next reaches ready (a failed start may be passing, such as a worker killed
    under memory pressure).
  - *Not claimed.* A worker is the same binary with the same privileges as qrvt; worker processes
    bound hangs and memory and keep a stuck engine from blocking qrvt, but they are not an operating
    system sandbox. The WebAssembly sandbox is still what confines the decoder.
  - *Compilation cache.* wazero writes each cache file to a temporary name and renames it, so workers
    compiling at the same time on first use are safe on Linux and macOS; on Windows a rename over a
    file in use can fail, and a failed cache write now means compiling again without the cache.

Rejected as a foundation: the pure-Go H.264 stack (`oops1/go.264`, `go-avkit/*`). An independent check
found it is one author's 2.5-month-old project that links glibc on Linux through purego and silently
switches to hardware codecs on Windows unless built with special tags, and that `go-avkit/avkit` loads
every sample of a file into memory.

Fallbacks if the spike shows the embedded engine is too slow:

- *Encode*: a small in-repository H.264 writer. Because every cell is exactly one 4x4 block of constant
  value, only DC coefficients are needed. `Eyevinn/hi264` demonstrates the approach for 8- and 16-pixel
  blocks.
- *Decode*: `gen2brain/h265`, a pure-Go HEVC decoder with outside contributors and conformance tests.
  Measured faster than the engine but not bounded in memory, it is only the opt-in HEVC fast path (see
  "Reading"). The engine decodes HEVC at 3.1-3.9 fps per instance at 4K (spike S1), below the 4 fps
  target, and that is accepted for version 1.

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
| `--resolution` | encode, calibrate make | `1080p` | Preset (`480p`, `720p`, `1080p`, `1440p`, `4k` or `2160p`) or `WxH`. For `calibrate make`, the monitor's resolution |
| `--fps` | encode | `12` | Display frame rate, 1-240 |
| `--mode` | encode | `gray4` | `gray4`, `bw` or `color` |
| `--hdr` | encode | off | Experimental 10-bit output; replaces `--mode`, and giving both is an error |
| `--protection` | encode | `normal` | `fast`, `normal` or `robust` |
| `--parity` | encode | `20` | Parity shards as a percentage of data shards, 5-100 |
| `--cell` | encode | `4` | Cell size in pixels: 4, 8, 12 or 16. The video size is reduced to a multiple of it, and the size used is reported |
| `--compress` | encode | `auto` | `auto`, `zstd`, `xz` or `none` |
| `--encrypt` | encode | off | Encrypt with a passphrase, prompted for unless `--passphrase-file` is given |
| `--sign-key` | encode | none | Sign with this private key |
| `--dry-run` | encode | off | Print video length, file size and expected filming time, then stop |
| `--passphrase-file` | encode, decode | none | Read the passphrase from the first line of a file instead of prompting. On encode it needs `--encrypt` |
| `--verify-key` | decode | none | Trusted public key, as a key file or the key line itself; may be repeated. When given, the transfer must be signed by one of them |
| `--max-size` | decode | 16 GiB | Largest total size that may be unpacked, with a unit: `B`, `K`/`KiB`, `M`/`MiB`, `G`/`GiB`, `T`/`TiB` (binary) or `kB`, `MB`, `GB`, `TB` (decimal) |
| `--max-entries` | decode | 100,000 (`DefaultMaxEntries`), with a manifest of at most 6,710,886 bytes (`DefaultMaxManifestBytes`, 64 MiB / 10) | Largest number of manifest entries that may be unpacked, 1-1,000,000; the manifest may then hold N x 64 MiB / 1,000,000 bytes, at most 64 MiB. A manifest over either limit fails with guidance naming this flag |
| `--overwrite` | decode | off | Allow existing files in the output folder to be replaced |
| `--state-dir` | decode | none | Keep progress for resuming and merging |
| `--report` | decode | none | Write a JSON report of the decode |
| `--ffmpeg` | all | `embedded` | `embedded`, `system` (`ffmpeg` and `ffprobe` from `PATH`) or the path of an `ffmpeg` (its `ffprobe` beside it, else from `PATH`). `system` and a path run outside the sandbox: for recordings the user made themselves. The help text says both |
| HEVC fast-path flag `--unsandboxed-hevc` | decode, inspect, calibrate read | off | Decode HEVC with the pure-Go `gen2brain/h265` (section 12). For recordings the user made themselves; it runs outside the sandbox, and its memory is not bounded against a crafted stream. The help text says all three. Present only in builds with the `unsandboxedhevc` tag until the decoder's assembly can be excluded; other builds refuse it |
| `--jobs` | all | CPU count | Worker count, 0-1024; 0 is the CPU count, reduced to fit memory |
| `--json`, `--plain`, `--quiet`, `--verbose` | all | | Output style. `--json` excludes `--plain` and `--quiet`; `--quiet` excludes `--verbose` |

- **Dashboard.** Built with Charm v2 (bubbletea, bubbles, lipgloss): source, video and signal panels, one
  bar per stage, rate and time remaining, and on decode a block map that fills in. When output is not a
  terminal it prints plain lines; `--json` prints one JSON event per line. Plain progress lines and
  warnings go to standard error and the summary to standard output; `--json` writes only to standard
  output, a start line, every event, then a summary or an error line, following the committed schema
  `internal/cli/ui/events.schema.json`, with nothing on standard error; a failure before the job
  starts, such as a usage error, is a single error line, and warnings are events. A hidden `qrvt demo` command plays a recorded decode through
  any of the three, for trying the display without a recording.
- **Output folder and inputs.** `-o` is required by every command that writes. Inputs, recordings, key
  files and `--ffmpeg` paths are checked before any work starts; every bad flag is a usage error naming
  the flag, the value and the accepted range.
- **Exit codes.** 0 success; 1 other failure; 2 bad command line; 3 not enough data yet (record more
  and try again); 4 wrong passphrase, or a bad or missing signature; 5 input refused (unsafe archive, a
  limit, an unsupported version); 130 stopped by the user.
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
    MaxEntries       int      // manifest entries that may be unpacked (flag --max-entries); 0: DefaultMaxEntries (100,000) and DefaultMaxManifestBytes (6,710,886); N: N x 64 MiB / 1,000,000 bytes of manifest; at most 1,000,000 and 64 MiB
    Overwrite        bool
    Report           string   // optional path for a JSON report
    Backend          string
    UnsandboxedHEVC  bool     // opt-in pure-Go HEVC decoding outside the sandbox
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
func (p Plan) CheckFrames(codewordsPerFrame int) (int, error) // Frames, or an error above MaxFrames
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
package directly; any other GUI runs the binary with `--json` and reads events. One exception, by
design: `func EngineWorker()` must be the first call in every host's `main`. In an ordinary process it
returns at once, having enabled engine worker processes (section 12); in a process started as a worker
it runs that worker's job and exits the process. A host that does not call it still works, with the
engine in process and slower, and decodes report a warning.

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
4. **Optics**: defocus and motion blur, vignetting, veiling and specular glare.
5. **Sensor and processing**: white balance, colour matrix, tone curve, interference fringes, noise,
   sharpening, colour noise reduction, clipping, 4:2:0. Fringes are applied here rather than in the
   optics because their amplitude is given in code values.
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

The table above does not name every parameter the stages need. These complete the presets:

| | `ideal` | `tripod` | `handheld` | `budget-android` | `iphone-hdr` | `harsh` |
| --- | --- | --- | --- | --- | --- | --- |
| Panel black, share of white | 0 | 0.1% | 0.1% | 0.2% | 0.1% | 0.5% |
| Panel response, ms | 0 | 4 | 4 | 4 | 4 | 8 |
| Capture-time jitter, ms RMS | 0 | 0.2 | 0.5 | 0.5 | 0.5 | 1 |
| Exposure gain, linear signal of display white | 1 | 0.9 | 0.9 | 0.9 | 0.265 | 0.9 |
| Colour matrix saturation boost | 0 | 0.1 | 0.2 | 0.3 | 0.2 | 0.3 |
| Colour noise reduction, share | 0 | 0.5 | 0.5 | 1 | 0.5 | 1 |
| Veiling glare, share of white | 0 | 0 | 1% | 2% | 1% | 3% |

How the parameters are read, and the values every preset shares:

- *Display.* The panel has the video's size unless set otherwise; the player scales bilinearly to fit,
  with black bars. Panel drive is 8-bit; light is black + (1 - black) V^(2.4 + gamma error). The panel
  refreshes in 1/60 s, top to bottom unless a scan rotation of 90, 180 or 270 degrees makes it run
  right to left, bottom to top or left to right (0 in every preset); a pixel's change is a linear
  ramp over the response time. Player tearing is not modelled. The backlight flickers at 240 Hz with
  the given relative amplitude. Sub-pixel stripes are off unless a test turns them on. The player may
  hold the last frame longer at the seam; every preset sets 0.
- *Timing.* A drop starts with the given probability per capture and loses one capture (bursts are a
  parameter, 1 in every preset). The readout is the time over which the rows' exposures start: row
  *y* of *H* (centre *y* + 0.5) starts at start + ((y + 0.5) / H) readout and is exposed for the
  exposure time. Capture-time jitter is Gaussian, clipped at 4 times its RMS.
- *Geometry.* A pinhole camera with focal length W / 1.4 pixels (70.0 degrees across) looks at
  the screen from the distance at which the untilted screen fills the given share of the frame width.
  The screen is tilted by an angle drawn uniformly up to the maximum, about an axis in its plane at a
  uniform direction; in-plane roll and mirroring are parameters, 0 in every preset. Shake is a random
  walk with one step per camera frame, steps of the given RMS in pixels (split evenly between x and
  y), pulled back by a factor 0.95 per step, plus a rotation whose steps move the frame's edge by
  about half as much; the pose is linear between steps, so a moving camera shears the rows of a
  rolling shutter. Distortion maps a capture point p, relative to the centre in units of half the
  frame width, to the pinhole point p (1 + k1 |p|^2); positive k1 is barrel.
- *Optics.* Defocus is a Gaussian; motion during the exposure widens it along x and y by the motion's
  length over the square root of 12. Corner vignetting falls off with the squared distance from the
  centre. Veiling glare is added light; the specular patch is a disc at 1.5 times white, at a random
  place on the code, with a smooth edge out to 1.25 times its radius.
- *Sensor.* Display white gives the exposure gain as linear signal, clipped at 1. White balance
  multiplies red by 1 + s and blue by 1 - s. The tone curve is gamma 1/2.4 into BT.709 limited range,
  or for `iphone-hdr` the BT.2087 BT.709-to-BT.2020 primaries matrix in linear light, then the
  BT.2100 HLG curve into 10-bit BT.2020 limited range. Fringes are a plane wave with a random period
  of 8 to 32 pixels and random direction. Noise is Gaussian (exact, tails included) and independent
  per colour channel. Noise sigma and fringe amplitude are in 8-bit full-range units of each R'G'B'
  channel, added after the tone curve; in limited-range brightness codes they come to about 0.64 and
  0.86 times the table's value (219/255 times the length of the BT.709 luma weights for independent
  noise, 219/255 for fringes, which are equal in the three channels). For 10-bit output they scale
  by 4. Sharpening is an unsharp mask of brightness against a 3 x 3 binomial blur;
  colour noise reduction mixes the 4:2:0 colour planes with a 5 x 5 binomial blur of themselves.
- *Recorder stand-in.* 8 x 8 DCT blocks with a uniform step of 3 / *b* code values, where *b* is the
  bitrate in bits per camera pixel per frame; the step is about 20 for `tripod`, 11 for
  `budget-android` and 23 for `harsh`. It is not calibrated against x264; it only lets unit tests exercise a lossy recorder.

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
| Setup | `scripts/setup.sh` | `scripts/setup.ps1` | Installs or verifies pinned Go from the official go.dev archive (SHA-256 pinned in the script), git through Homebrew, apt, dnf, pacman or winget, and the pinned development tools through `tools/dev install-tools`. Native ffmpeg and wasmtime are optional |
| Build | `scripts/build.sh` | `scripts/build.ps1` | Static build for the host, or all six targets |
| Test | `scripts/test.sh` | `scripts/test.ps1` | Unit and integration tests with the coverage gate; options for fuzzing, the simulator matrix and real recordings |

The setup scripts stand alone, because Go may not be installed yet: they install or verify Go itself and
then run `go run ./tools/dev install-tools`. Go comes from the go.dev release archive rather than a
package manager, because distributions lag and cannot pin a patch release; the archive's SHA-256 is
pinned in the script, and `GOTOOLCHAIN=local` stops an older Go from fetching a toolchain on its own.
With `--spike` they also install the two tools only spike S1 needs: a native ffmpeg with an HEVC encoder
(libx265, from the package manager or winget) and the wasmtime command-line runner (from its release
archive, SHA-256 pinned). They print what they are about to install, and ask, before installing
anything. The build and test scripts are thin wrappers around
`go run ./tools/dev <task>`, so that logic exists once. CI calls the same scripts on all three systems.

`install-tools` downloads golangci-lint, GoReleaser, syft (the SBOM generator) and actionlint as
release archives whose SHA-256 is pinned in `tools/dev/tools.go`, and builds go-licenses and
govulncheck, which publish no binaries, with `go install`; `-only` installs a subset. Building golangci-lint and
GoReleaser from source took 2-5 minutes per CI runner and once hung for 55 minutes; a checked download
takes seconds. Other `tools/dev` tasks: `licenses` (licence compatibility), `vulncheck` (govulncheck
over the release build), `check-workflows` (the
action-pinning and permissions policy, then actionlint), `release-snapshot` (the whole release without
publishing, then a check that every target and package format was produced) and `sign-windows` (called
by GoReleaser).

## 17. CI and release

- **Each pull request** (`ci.yml`): lint; tests on Linux, macOS and Windows for amd64 and arm64; race
  detector; coverage gate; vulnerability scan; static-build check for all six targets; release snapshot;
  licence-compatibility check; workflow policy and actionlint; short fuzz runs (10 s per target); docs
  build once the website exists (M8). Builds and ordinary tests run with `CGO_ENABLED=0`. The race
  detector needs cgo on most systems and does not support windows/arm64, so race tests run with cgo on
  the other five runners. Tests, the coverage gate, race tests and fuzzing are built with the release
  build tags (`RELEASE_TAGS`), so CI tests what ships; one amd64 runner also tests the untagged build.
  Lint runs on both builds. The setup scripts run with `--spike` on Linux, macOS and Windows runners,
  and `setup.sh` without it in Fedora and Arch Linux containers, so that every package-manager path and
  the Go download are exercised. The container images are pinned by digest and refreshed by hand at the
  start of each milestone, and Fedora's also when a new release comes out (spring and autumn); the
  checkout is mounted read-only and the containers run with `no-new-privileges`. One step runs the
  `internal/video` tests with `-tags unsandboxedhevc,noasm,nounsafe` on every runner, since only that
  tag compiles the HEVC fast path; the fuzz task adds the tag, so `FuzzFastPathNAL` is fuzzed with the
  others. Every job and every long step has a timeout.
- **Nightly** (`nightly.yml`, also startable by hand): long fuzzing (5 minutes per target by default),
  keeping the grown corpus in the Actions cache between nights and uploading any failing input; the full
  simulator matrix (M3), a 1 GB loopback with memory assertions (M6) and benchmarks with regression
  alerts (M6) run as soon as their `tools/dev` tasks exist and skip with a notice until then.
- **Security**: CodeQL (`codeql.yml`, security-extended queries, on pull requests, on main and weekly);
  Dependabot for Go modules and actions, except wazero and go-ffmpreg: every engine speed, memory and
  sandbox figure of spike S1 was measured on their pinned versions, so they are updated by hand, with
  stream D re-running the S1 measurements, before the final security pass (M7) so that the pass
  covers the shipped engine. An advisory for either module, whether from a GitHub alert or from the CI
  vulnerability scan, is fixed by hand straight away, with the S1 measurements re-run. govulncheck over
  the default build (the
  action, on all six runners) and over the release build (`tools/dev vulncheck`, for linux, darwin and
  windows). Every action is pinned by a full commit SHA with a
  version comment, every workflow declares top-level permissions (jobs widen them only where needed),
  and nothing runs on `pull_request_target`; a `tools/dev` test enforces this and CI also checks each
  comment's tag against the pinned commit. The licence check (go-licenses) allows only licences whose
  code may be distributed under GPL-3.0-or-later; GPL-2.0 is not on the list, because the tool cannot
  tell GPL-2.0-only from GPL-2.0-or-later. Vulnerabilities are reported privately (SECURITY.md section
  11).
- **Release**: release-please (`release-please.yml`) keeps the changelog and version from conventional
  commits; merging its pull request creates the `v*` tag and the GitHub release. It stays off until the
  owner enables it. A `v*` tag runs GoReleaser (`release.yml`, `.goreleaser.yaml`): tar.gz archives (zip
  on Windows) holding the binary, LICENSE, NOTICE and README; checksums; a keyless cosign signature of
  the checksum file; an SPDX SBOM per archive; build provenance (`actions/attest`) for every file in
  the checksum file; Homebrew cask, Scoop manifest and winget pull request; deb, rpm and apk packages;
  macOS notarisation and Windows signing. Steps whose secrets are absent are skipped. The release
  build's Go build tags are defined once, as `RELEASE_TAGS` in `.goreleaser.yaml` (`noasm,nounsafe`),
  and `tools/dev build` uses the same set.
- **Docs**: a website built from `docs/` and published to GitHub Pages on each release
  (`pages.yml`, a skeleton until M8), containing the user guide, this design, the format specification
  and an architecture guide.

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
| M7 | Final security pass |
| M8 | Website, packaging, signing, release dry run, a delta security review of everything since M7, then the first release candidate |

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
| 2026-11-13 | M0 review: section 16 says Go comes from the pinned go.dev archive (SHA-256 checked) rather than a package manager, and wasmtime from its release archive; section 14 gains `Plan.CheckFrames` and `fec.MaxFrames`, by which an encoder refuses more than 4,194,300 frames (`Frames` is unchanged) |
| 2026-11-13 | M0 spikes S1 and S3: section 3 records the wazero against wasmtime outcome (1.22-1.30x, continue in Go) and that HEVC is decoded outside the sandbox; section 4 (F3) says which x264 settings give 8.4 bits per cell; section 5 records the HDR output measurements (libaom AV1 10-bit unusable, H.264 High 10 the candidate, choice in M6); section 8 records the LDPC decoder as built and its measured speed (threshold not met), forbids Go experiments and amd64-only packages in release builds, and sizes the default worker count on the worst measured engine memory; section 12 gives the tuned x264 settings, even-length encode runs, the pure-Go HEVC path with its recover boundary, limits, engine fallback and the `noasm` tag problem, lazy module loading, and reserved linear memory |
| 2026-11-13 | M0 review: section 12 runs the HEVC decoder with `Threads(1)` inside per-slice recover boundaries, checks parameter sets before decoding (4:2:0, 8 or 10 bits, at most 7680 x 4320, a buffer of at most 6 pictures, justified from HEVC Annex A.4.2), states the 10 s limit abandons rather than stops a decode, and plans the M1 measurement for the `noasm` tag; the engine reservation is 512 MiB and equals its ceiling; section 8 sizes workers on the larger worst case, 920 MB |
| 2026-11-13 | M0 review round 2 and the owner's decision for version 1: every codec, HEVC included, is decoded by the sandboxed engine; the pure-Go `gen2brain/h265` is only an opt-in HEVC fast path, because it allocates a full picture for every missing reference a slice names (about 6.5 GB at 8K), so the 720 MB bound is withdrawn. Section 3 records this; section 8 does not count the fast path; section 11 gives the default decode time (about 2.8-3.6 times the duration for 4K60 on 8 cores, 25-40% slower per core than the fast path) and the two speed-ups; section 12 gives the engine's 4K HEVC figures, keeps skipping the loop filter off, and lists the fast path's protections (exactly `Threads(1)`, the per-sub-layer `sps_max_dec_pic_buffering_minus1 + 1 <= 6` check, `-c copy` before decoding, a stop at the first abandoned decode) as reducing risk, not bounding it; section 13 adds the fast-path flag |
| 2026-11-13 | M0 review round 3: section 12 requires `WithCloseOnContextDone(true)` on the runtime qrvt creates, adds the 30 s no-progress watchdog with the M1 stream D cost measurement, gives the stream-copy instance's mounts, and states that the HEVC fast-path flag does not ship until the decoder's assembly can be excluded |
| 2026-11-13 | M0 follow-up: section 12 gives the single-file read-only view as an `fs.FS` mount that counts reads, the copy instance's output directory, how the watchdog measures progress for probing (reads) and copying (output-file growth), and the `MaxDecodedPixelsPerInputByte` limit (M1 stream D) |
| 2026-11-13 | M1 setup: the HEVC fast-path flag is named `--unsandboxed-hevc`; section 14's `DecodeOptions` (and the root package's `AnalyzeOptions` and `InspectOptions`) gain `UnsandboxedHEVC` |
| 2026-11-13 | M1 stream E: section 15 moves interference fringes from the optics stage to the sensor stage (their amplitude is in code values), completes the presets with a second table (panel black and response, capture jitter, exposure gain, colour matrix, colour noise reduction, veiling glare), and states how each parameter is read and the values every preset shares (panel scan-out, flicker frequency, field of view, shake walk, distortion convention, glare patch, recorder stand-in step) |
| 2026-11-13 | M1 stream E review: section 15 adds the panel scan rotation (0 in every preset) and says player tearing is not modelled; noise is exact Gaussian and jitter is clipped at 4 times its RMS; HLG output applies the BT.2087 primaries matrix first; noise and fringe amplitudes are stated as per-channel full-range units (about 0.64 and 0.86 times in brightness codes); the field of view is 70.0 degrees; the readout wording and the quantiser-step figures are corrected |
| 2026-11-13 | M1 stream B: section 8 describes the encoder and the decoder as built (int16 bounds, pinned shortened bits, check before the first iteration), records the production decoder's speed beside the S3 decoder, and records the error-rate test: published FER 1e-2 points for the four 802.11n n = 1944 mother codes with their sources, our measured points (all within 0.2 dB), and the shortened codes' points; section 12 records the `noasm`/`nounsafe` measurement and the decision that the whole release can be built with `-tags noasm,nounsafe` |
| 2026-11-13 | M1 stream B, review of PR #8: section 8 checks the four shipped inner codes against the binary-input AWGN capacity limit with the rate counted with the CRC (codes 1-3 within 2.5 dB; `robust` 2.60 dB away, missing that criterion by about 0.1 dB because of the frozen shortening; corrected below: the cause was the decoder), corrects the Es/N0 values for the CRC, and states the shard store's sparse file on Windows and its 64 GiB stream cap; section 12 limits the `noasm,nounsafe` claim to the three `klauspost` libraries and calls Reed-Solomon reconstruction marginally below the bar, accepted because it is 30-50 times faster than LDPC decoding |
| 2026-11-13 | M1 stream B, designer's follow-up on PR #8: section 8 compares the shipped decoder with a test-only sum-product reference and min-sum variants on the four inner codes, and corrects the cause of `robust`'s capacity gap: the decoder (integer truncation about 0.3 dB, normalisation x0.75 about 0.6 dB), not the shortening; the shipped decoder is unchanged pending the designer's decision |
| 2026-11-13 | M1 stream B, designer's note on PR #8: section 12 states that Reed-Solomon reconstruction with `noasm` measured 45-49 MB/s per core in the worst case, below the bar of about 50 MB/s, and why the tags are accepted anyway (seconds of Reed-Solomon against hours of video decoding; 30-50 times faster than one core of LDPC) |
| 2026-11-13 | M1 stream G: section 16 says `install-tools` downloads the pinned tools as release archives with pinned SHA-256 (go-licenses through `go install`) and lists the new `tools/dev` tasks; section 17 names the workflows, the nightly placeholders, the workflow policy and its test, the licence allow-list (GPL-2.0 excluded), release-please being off until the owner enables it, provenance for every checksummed file, and `RELEASE_TAGS` in `.goreleaser.yaml` as the one definition of the release build tags, set to `noasm,nounsafe`, with which CI tests, measures coverage and fuzzes (one runner also tests untagged) |
| 2026-11-13 | M1 stream G follow-up: section 16 adds the `vulncheck` task and govulncheck v1.9.0; section 17 scans the release build with govulncheck, lints both builds, and runs `setup.sh` in Fedora and Arch Linux containers |
| 2026-11-13 | M1 stream G follow-up: section 17 says Dependabot ignores wazero and go-ffmpreg, which are updated by hand with the S1 measurements re-run (stream D) no later than M7 |
| 2026-11-13 | Decoder follow-up to M1 stream B (designer's decision): section 8 records the measured candidates (offset min-sum 1-3 steps, normalised x13/16 with soft bits x4 inside, M1 and sum-product) at soft-bit scales 0.5, 1 and 2, FER 1e-2 and 1e-3, wrong-codeword counts and the choice of x13/16 for all codes; the decoder description, the measured points of the mother and shortened codes (`robust` now 1.75 dB from capacity, meeting the 2.5 dB criterion) and the speed are updated. No format bit changes |
| 2026-11-14 | Review of PR #14: section 8 replaces the FER 1e-3 statement with the paired comparison (`TestPairedM1B`, 400,000 frames per point): B is significantly behind M1 near FER 1e-3 on code 3 and `normal` (about 0.01 dB, z up to +6.1) and better on `fast` and `robust`; the tolerance is the designer's decision, pending |
| 2026-11-14 | Designer's decision on PR #14: section 8 records that candidate B was accepted with an explicit tolerance (at FER 1e-3 within 0.02 dB of the previous decoder on every code), the measured losses near FER 1e-3 (0.011 dB on code 3 and `normal`) and the 0.87 dB gain on `robust` |
| 2026-11-13 | M1 stream F: section 13 resolves what the command line left open: the `--resolution` presets (with `2160p` as another name for `4k`) and its use by `calibrate make` for the monitor resolution; ranges for `--fps` (1-240) and `--jobs` (0-1024, 0 the CPU count); `--hdr` replaces `--mode`; `--passphrase-file` reads the first line and needs `--encrypt` on encode; `--verify-key` takes a key file or a key line; the `--max-size` units; which output flags exclude each other; where plain, summary and JSON output go and the JSON schema; the hidden `demo` command; required `-o`; and the exit codes |
| 2026-11-13 | M1 stream F, PR #11 review: section 13 says that with `--json` nothing goes to standard error, that a failure before the job starts is a single error line, and that warnings are events |
| 2026-11-14 | M1 stream F, PR #11 round 2: section 13 adds `--max-entries` (the owner's decision: default manifest limits about a tenth of the format's, raised by one flag), mapped to `DecodeOptions.MaxEntries`, which section 14 shows |
| 2026-11-14 | M1 stream F, PR #11 round 3: sections 13 and 14 give the exact manifest defaults, the root constants `DefaultMaxEntries` (100,000) and `DefaultMaxManifestBytes` (6,710,886, 64 MiB / 10), and the scaling rule of `--max-entries` (N x 64 MiB / 1,000,000 bytes, at most 64 MiB) |
| 2026-11-14 | The owner swapped the last two milestones: M7 is the final security pass and M8 the website, packaging, signing and release dry run (section 18 and the M7/M8 references in section 17); the engine pins are updated before the security pass |
| 2026-11-14 | Section 10 step 7 names the soft-bit scale (`modem.LLRScale`, a quarter nat); a repeated sentence in section 8 removed |
| 2026-11-13 | M1 stream D: section 8 caps the worker count at `video.EngineInstances()` (GOMAXPROCS - 1), because compiled Wasm cannot be preempted and instances on every P starved the watchdog on the 4-vCPU CI runners. Section 12 records the engine as built: the embedded copy of the module and its hash check, the encoder's MP4 and even-length `.h264` runs with `Concat`, how slices are read (1 s preroll with `-noaccurate_seek`, `-copyts`, `showinfo` timestamps, `-fps_mode passthrough`, source pixel format, size changes refused), the instance cap, and the measured cost of `WithCloseOnContextDone` (75-91% on decoding, reported to the owner; kept on); the HEVC fast path is compiled only with the `unsandboxedhevc` tag, sends parameter sets that differ from the probe to the engine, takes timestamps from `framemd5`, and times each picture from its first slice. Section 13: the `--ffmpeg` row says where `ffprobe` comes from and that a path also runs outside the sandbox; the `--unsandboxed-hevc` row says the flag exists only in tagged builds |
| 2026-11-14 | M1 stream D, review of PR #9: section 8 calls the instance cap a precaution and states that workers must not hold more decoders open than the cap without reading them. Section 12: how a slice is read adds the anchored `showinfo` match, the refusal of extra timestamp lines and of odd sizes, and reading frames straight into the caller's planes; the sandbox adds the protocol and format whitelists (the HLS playlist that held ffprobe for 8 minutes) and the probe's bounds; the watchdog counts a complete, timestamped frame, does not excuse an instance qrvt is waiting on, closes the pipe on cancellation, and gives the true reason the cap is not needed with close-on-cancel and its hold-and-wait hazard; the fast path's as-built list adds the copy-size bound, the abandon without a flush, the CI step and the fuzz tag |
| 2026-11-14 | M1 stream D, CI on PR #9: sections 8 and 12 correct the instance cap's description: on the macos-26-intel runner GOMAXPROCS + 1 uncapped spinning instances took 16.8 s against a 2 s limit, so the cap is a mitigation, not a harmless precaution |
| 2026-11-14 | M1 stream D, round 2 of the review of PR #9: section 8 calls the instance cap a real defence (uncapped, stops were up to 8 times late on a CI runner); section 12 adds the 8K UHD capture limit in either orientation, corrects the `showinfo` injection wording (an unknown Matroska CodecID is logged from the start of a line) and adds the frame-number sequence check and the slice-end count check, and restates the cap's evidence |
| 2026-11-14 | M1 stream G follow-up 2: section 17 states that an advisory for wazero or go-ffmpreg (GitHub alert or CI scan) is fixed by hand straight away with S1 re-run, how the Fedora and Arch Linux container digests are refreshed and that the containers run read-only and without new privileges, and adds the HEVC fast-path test step and fuzz tag (stream D, PR #9) |
| 2026-11-13 | M1 stream D, worker processes (owner's decision after the close-on-cancel measurement): section 8 re-derives the worker count for workers (no GOMAXPROCS cap; 920 MB each, measured 190-403 MB) and keeps the cap for instances in qrvt's process; section 12 adds worker processes (protocol, isolation, termination, fallback, what is not claimed, the compile cache) and keeps encoding in process with close-on-cancel off (15% cost through a worker); section 14 adds the `EngineWorker` hook as the one exception to "never exits the process" |
| 2026-11-14 | M1 stream D, review of PR #15: section 8 marks the worker memory figures as measured on one machine (the S1 development machine), not on the CI runners, and counts `Concat` among the in-process instances. Section 12: the worker speed figures are marked the same way, with the second run's figures; the job carries a protocol version, which a worker of another version refuses and the parent takes for a failure to start; standard input is sent only after the ready message; on Unix the worker closes every inherited descriptor above 2 as it starts; on Windows it is created suspended and resumed only once in its Job Object; the loading phase is bounded by `video.EngineLoadLimit`, and compiling in qrvt's process has no limit but the context; encoding and `Concat` stay in process with no forced stop, stop on cancellation by having their input closed, and delay (or, spinning, would stall) qrvt's own garbage collection; the watchdog is no longer described as covering encoding |
| 2026-11-14 | M1 stream D, round 2 of the review of PR #15: section 12 treats anything other than the ready message as a worker's first message (a version refusal, any other exit, a broken or early message, the worker ending) as a failure to start, with nothing passed on and the job run in process |
| 2026-11-14 | M1 stream D, review of PR #20: section 12's fallback is reported per backend by `(*video.Embedded).InProcess` (the pipeline warns once per decode), and `video.WorkerStatus` is the process's current state, cleared when a worker next reaches ready |
