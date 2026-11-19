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
| F4 | Blur moves the best cell size by about one camera pixel | Sigma 1.0 to 1.5: four greys at 5.1 camera pixels per cell drop from 1.7 to 1.5 bits (code-value blur; with blur in linear light and sharpening the loss is larger, section 11 condition 2, 2026-11-18) |

**Rule of thumb:** net throughput is 13-16% of the recording's bitrate. An iPhone recording 4K60 HEVC at
about 53 Mbit/s therefore carries roughly 0.9 MB/s at best. This is section 4's simulation, and it
is superseded for planning: the handheld planning rates of section 11, re-derived on 2026-11-18, are
about 280 KB/s at 4K60, and S2's real recordings gave about 3.3% of the bitrate at a setting
decodable with margin.

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
| Four greys | `--mode gray4` | 2 | 3.5 (section 4; `normal` re-checked at 3.6, 99.7%; `fast` and `robust` not re-derived) / 6 (`normal`; 4.8 `robust`; `fast` not established, 10 provisionally) | 8.4 | Provisional default |
| Black and white | `--mode bw` | 1 | 2.6 / 3.4 | 7.3 | Most robust |
| Eight colours | `--mode color` | 3 | 4.3 / 5.5-6 | 28 | Best when the recording bitrate is generous |
| Eight greys, 10-bit | `--hdr` | 3 | not simulated | not simulated | Experimental |

The handheld figures for four greys were re-derived on 2026-11-18 (section 11, condition 2). The
other figures are those of the simulation of section 4.

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
| Four finders, 14 x 14 cells plus separator | Concentric squares giving a 1:1:3:1:1 signature at any scale or angle. The bottom-right one is inverted to fix rotation; a decoder assumes the capture is not mirrored | 0.8% |
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
   If two copies give different frame indices, both are kept. If none decodes, the capture is skipped:
   in M2 the detector does not return a quad without a decoded header (section 10, step 1). Inferring
   the index as the last index plus 0 to 3, chosen by the phase markers, when fewer than four frame
   periods have passed, is deferred to M5, where a tracked quad could carry the header from a
   neighbouring capture (designer ruling, 2026-11-15; section 18).
2. **Phase field.** At each anchor, the four quadrant intensities are linearised using the anchor's own
   black and the palette's gamma and divided by their own total, which is the white reference because the
   weights sum to one (the anchor's white serves when that total is below half of it); the result is
   projected onto non-negative weights summing to one, one per value of frame index mod 4. Interpolating
   bilinearly across the lattice, with missing anchors predicted linearly from their neighbours, gives
   per-cell weights. This handles boundaries in any direction, player tearing and three-frame blends.
   Measured in M2: dividing by the anchor's white instead let fringes of 24 code values make clean
   anchors look mixed, so that two of three recordings had no codeword left to decode. The per-cell
   weight error reaches 0.08 where a blend spans only a few lattice steps and 0.15 beyond the outermost
   anchors; the owner accepted this on 2026-11-15 (section 15). A cell the field misjudges is at worst
   left out or credited with a little of the next frame in it; it is never credited to the wrong frame.
3. **Assignment.** A cell contributes to code frame *k* only if the weight for *k* mod 4 is at least 0.92.
   Otherwise its soft bits are zero. Measured in M2, about 1.4% of truly clean cells (true weight at
   least 0.92) are left out this way, and every credited cell's true weight is at least 0.80.
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
9. **Variable frame rate.** Timestamps are never trusted for identity, only for diagnostics and, once
   M5 adds rule 1's inference of an undecoded index, for its bound of four frame periods.
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
so the worker count is not reduced below the CPU count: a decoding worker spends about 300 ms of engine
time per 4K frame, against a few milliseconds of qrvt's own work on the same frame in the parent
(the frame's copy through the pipe, 10-37 ms at 4K, overlaps the next frame's decoding), so one
worker per core keeps every core busy, and vision on the same cores only fills the gaps. A worker's
peak resident memory measured 190 MB (1080p H.264), 310 MB (4K HEVC 8-bit) and 403 MB (4K HEVC
10-bit), on one machine (the S1 development machine, spike S1 section 13), not on the CI runners;
920 MB per worker stays the conservative count, because the whole 512 MiB reservation can
become resident (spike S1). On a machine with 16 GB and 16 cores that allows 8 workers.
`internal/video` enforces that count itself, as a cap on concurrent worker processes
(`video.WorkerProcesses()`: GOMAXPROCS at start-up, which honours a container's CPU quota, and the
memory this process may use: Linux `sysinfo`, or the cgroup's limit when smaller (v2 `memory.max`,
v1 `memory.limit_in_bytes`), macOS `hw.memsize`, Windows `GlobalMemoryStatusEx`; GOMAXPROCS alone
when the memory is unknown), so every caller and every test gets it. A job waiting for a turn is
waiting, not stalled: the wait counts against no bound but its context. PR #15 left worker
processes uncapped, reasoning that the operating system schedules them; that is withdrawn, since
uncapped workers can oversubscribe the CPUs. It was prompted by a probe of a small clip, about a
second's work, that exceeded the then 15 s `EngineProbeTime` on the 4-vCPU macos-26-intel runner
under the full test load (run 43629256993); the cause is believed to be CPU starvation, but was not
shown. The cap limits starvation from this process's own workers only, not from other processes on
the machine, and it did not remove the effect: a capped run on that runner still recorded a probe
of 13 s (run 43633299696). The probe bound is now 60 s for that reason (SECURITY.md section 5).
Instances in
qrvt's own process (encoding, `Concat`, and every instance when no worker process can be started) are capped
at `video.EngineInstances()`, one fewer than GOMAXPROCS (at least one). The cap is a real defence:
uncapped, stops were up to 8 times late on a CI runner (section 12). `internal/video` enforces it
itself. Under either cap, further jobs wait their turn with no limit but the context, so a worker
must not hold more decoders open than the cap without reading them (`TestHoldAndWait`). The opt-in
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
   As built: a pixel is dark when it is below lo + 0.7 (hi - lo), lo and hi being the minimum and
   maximum of a window about a twentieth of the downscaled width across; a local mean failed beside
   the dark surround of the screen. The threshold sits at about half the light in linear terms, not
   at the code-value midrange (about a fifth of it): blur acts on linear light, and at the midrange
   it widened the light rings and erased the thin outer dark ring at 4 to 5 pixels per module. For
   the same reason the flanks need only a quarter module, not half. A row hit is confirmed along its
   column and, loosely, both diagonals (every line through concentric squares crosses them in the
   same ratios), and each candidate's ring is verified at full resolution, where its centre is
   refined from the edges of ring 2, found midway between the extremes of rings 2 and 3, along seven
   rows and seven columns. The four are chosen as a convex quad in clockwise order on the image, with
   module sizes within a factor of 3 (25 degrees of tilt gives about 2) and left and right sides of
   126 modules within a factor of 1/0.65. Data cells mimic finders at the finder's own scale (on a
   grid of 135 rows or fewer a module is at most one cell; on a 240 x 135 grid 1 to 4% of captures
   gave a wrong quad), so the six best quads are tried in turn and a quad is reported only when its
   header decodes (step 2) and the anchors agree with every finder. The decoded grid must imply at
   least 1.5 camera pixels per cell between the finders, which bounds the layout built for it by the
   image's size; when the copies decode headers with different grids (a false CRC pass, or crafted
   content), each grid is tried, the one more copies give first; layouts are cached by grid and at most two
   distinct grids are used per capture, on the three-finder path too. With that grid the anchors are located as in step 3, giving up after 64 tried with
   fewer than eight found (a header claiming the largest grid the gate allows at 4K then costs about
   19 MB and about 200 ms per capture at 4K, 3.5 times the cost reference of SECURITY.md section 9.1); at least eight, and a quarter of those inside the capture, must be
   found, and each finder in turn must lie within half a finder module of where a model fitted to
   the located anchors and the other three finders puts it. The check is not there to catch a real
   finder slightly misplaced: the detector locates a real finder's centre to about a pixel (on the
   S2 laptop recording, the cluster sat within a pixel of the drawn finder's centre while the
   anchors' model put it 2.5 cells away). It is there to catch a different, finder-like pattern
   standing in for the finder, and two finder-like patterns cannot both have their centres within
   half a module of the same point; a visible finder more than half a module from the truth is
   also what makes a quad wrong. On fine grids that leaves no margin below the wrong-quad line: a
   finder-like pattern just beyond half a module from a hidden finder's place, standing in for it,
   could be accepted, and such a quad then relies on the header's CRC and the codewords' CRCs. In cells this is looser on fine grids (1 cell at 1080p with
   4-pixel cells, where it was already 1; 1.85 cells on an 800 x 500 grid, where it was 1) and
   tighter on coarse ones. The S2 recordings motivated revisiting it: until then a finder had to lie
   within the smaller of one cell and half a module, and on rescaled playback the corner finders,
   beyond the anchors' reach, were rejected at 0.55-0.78 cell (most of that came from the k1-only
   lens model, step 3). The three-finder path fits its leave-one-out model with k1 alone: with one
   finder only predicted, k2's freedom let a decoy among the visible three bend the model until the
   real finder beside it disagreed too, and the narrow fix below then left that real finder out.
   One copy can decode with one finder wrong (it happened once in 200
   poses, with a finder 20 cells off), since a copy is placed mostly by the finders nearest it. An
   earlier rule, requiring one of the two anchors next to each finder, rejected 17 of 200 real harsh
   captures whose anchors there were unreadable. A capture whose four copies all fail is not located
   (section 7, rule 1); on the simulator's presets that never happened (step 2's 1,000 of 1,000).
   **Three finders.** When no four confirm (a finder under glare or an overlay), the code is located
   from three, whichever corner is hidden, the inverted one included. The hidden centre is predicted
   from the other three and their module sizes: under a homography a point is N/w with N and w
   affine in the plane, and lengths scale as w^(-3/2), so w at each visible finder follows from its
   module size and w and N at the fourth from the parallelogram rule. (The parallelogram alone was up
   to 385 pixels off at 4K; this, within about 50. Module sizes are taken as the mean of the ring's
   inner and outer spans, which blur moves oppositely, so normal and inverted finders agree.) A
   header copy must then decode alone with the prediction or a point within 2.5% of the diagonal of
   it; the anchors are located from the three visible finders and move the hidden centre twice to
   where they and the three finders put it; and the quad must pass the four-finder checks above
   (header, anchors, each visible finder agreeing within the tolerance), with at least four anchors located
   in the quarter of the grid nearest the hidden finder. A quad is never accepted on three finders
   alone; with two hidden, nothing is located. To bound the cost on hostile frames the three-finder
   path runs on the last scan only and tries three candidate quads, built without the four finders of
   a rejected four-finder quad whose header decoded a grid failing the 1.5-pixel gate; a gate
   failure thus counts against its own quad, not against another code in the same capture. A
   finder that disagreed with the anchors in the four-finder check stays available: it may be a
   real finder damaged by an overlapping decoy. A finder is left out only within the three-finder
   path, after a candidate built on it was rejected because it disagreed with the anchors, so that
   quads built on a decoy beside a real finder do not take all three tries. Measured on two decoy
   studies, long tests on a 480 x 270 code that never give a wrong quad (designer's rule,
   2026-11-15: keep this only if neither study gets worse than before):

   | Study | Cases | Not located: finders excluded after the four-finder check (round 2) | none excluded (round 3) | this rule | this rule with the S2 geometry fix |
   | --- | --- | --- | --- | --- | --- |
   | `TestDecoyMatrix`: every finder visible, a copy of one finder (with or without its separator, either polarity) 0 to 16 cells inward horizontally, vertically or diagonally | 816 | 30 | 20 | 18 | 14 |
   | `TestDecoyMatrixReview`: a copy with its separator (either polarity) 1 to 8 cells left, right, up or down, the real finder covered or visible | 512 | 24 | 30 | 10 | 3 |

   A known limit, left to M5: the cases not located before the S2 geometry fix were decoys of normal polarity 4 to 6
   cells diagonally or vertically inward at the bottom-left and top-right, 7 or 8 cells right of a
   covered top-left finder, 6 to 8 cells below the top-right finder, covered or visible, and 10 cells
   right of the top-left finder or diagonally in from the top-right; and inverted decoys 7 or 8 cells
   beside a top finder. All fail safe, not located.
   The measured limit below (the table of codes located, at commit 2ec594f) predates this change.
   The hidden centre is an estimate. The tests judge
   quads by the designer's line: a visible finder more than half a module from the truth makes the
   quad wrong; only the hidden finder may be up to one module off, and only when the header reads,
   the mesh fits and the capture's cell-centre error is at most 0.15 cell, which makes it imprecise
   (reported, and asserted at most 3% of the located quads). With a glare patch of 2% of the code
   area on each finder in turn, 200 handheld 4K poses each: 200 of 200 located and read, none
   imprecise, no wrong quad or header, cell-centre error outside the patch 0.019 cell RMS
   (four-finder captures: 0.019).
   **Measured limit** (`TestDetectModuleSweep`, measured at commit 2ec594f): a 240 x 135 grid,
   whose finder module is one cell, filmed at 1920 x 1080 with each preset's own blur, 200 poses per
   size (seeds 7000 to 7199). Codes located, exact or imprecise, by untilted camera pixels per cell,
   with the wrong quads in brackets where there were any:

   | Preset (blur) | 2.8 | 3.2 | 3.6 | 4.0 | 4.4 | 4.8 | 6.0 | 7.2 |
   | --- | --- | --- | --- | --- | --- | --- | --- | --- |
   | tripod (1.0) | 200 | 200 | 200 | 200 | 200 | 200 | 200 | 200 |
   | handheld (1.5) | 19 (2) | 186 (4) | 199 (1) | 200 | 200 | 200 | 200 | 200 |
   | budget-android (1.5) | 19 | 191 | 200 | 200 | 200 | 200 | 200 | 200 |
   | iphone-hdr (1.5, 10-bit HLG) | 0 | 76 (1) | 158 (1) | 194 | 200 | 200 | 200 | 200 |
   | harsh (2.0, tilt 25, glare 2%) | 0 | 0 | 1 | 17 | 65 | 101 (1) | 169 | 158 |

   At most 1 of a column's located quads was imprecise (harsh at 6.0 and 7.2). The wrong quads were the
   code with a centre between half a module and about 0.75 module off (their headers read
   correctly), counted wrong by the line above. The cell-centre error at 4.0 pixels per cell was
   0.044 (tripod), 0.086 (handheld), 0.076 (budget-android) and 0.124 (iphone-hdr) cell RMS.
   **Limit (accepted trade-off):** the three-finder path's cost limit (it runs on the last scan only) located fewer codes below the
   reliable sizes than before them, most for harsh at 1080p (184 and 181 at 6.0 and 7.2 before, 169
   and 158 now); harsh at 4K is unchanged at 997 of 1,000. The
   smallest cells at which the code is reliably located (at least 199 of 200, none wrong), against
   the planning point of 5 camera pixels per cell (section 11):

   | Preset | Smallest reliable, camera pixels per cell | Margin at 5 |
   | --- | --- | --- |
   | tripod | below 2.8 | over 1.8 times |
   | handheld | 4.0 (3.6 gave 199 with 1 wrong) | 1.25 times |
   | budget-android | 3.6 | 1.4 times |
   | iphone-hdr | 4.4 | 1.1 times |
   | harsh | not reached at 1080p | — |

   **Limit:** harsh is never reliably located at 1080p: at most 169 of 200, at 6 pixels per cell (a
   finder and its surroundings under the glare patch, or the code out of frame at 25 degrees and fill
   0.75 or more). At 4K, 997 of 1,000. Always-on tests assert every pose at 4.0 (handheld) and 4.8
   (iphone-hdr) (`TestDetectModuleLimit`). On grids of 270 rows or more a finder module is two cells
   or more, so there the limit on cells comes from the anchors and the sampling, not the finders.
   **Departure from "at most 1280 pixels wide", accepted:** when no quad is found in a downscaled
   capture, the scan is repeated once one step finer (1920 pixels wide for 4K). At 25 degrees of tilt
   a far finder can measure under 3 pixels per module at 1280 pixels wide. On 1,000 simulated 4K poses
   of the handheld preset tilted up to 25 degrees (seeds 10000 to 10999), the 1280-pixel scan alone
   found 997 quads; with the rescan, 1,000. Tripod and handheld found 1,000 of 1,000 either way.
   Clockwise order assumes a capture that is not mirrored (section 4.1 of FORMAT.md); a mirrored
   capture swaps the top-right and bottom-left finders and its header does not decode. Mirrored
   recordings are left to M5.
2. **Read the header.** Fit a homography to the four finder centres and sample the header slots at their
   fixed fractions, using the adjacent finder's black and white as references. Soft Viterbi decoding per
   copy, then per pair, then all four.
   As built: barrel distortion bows the frame edges between the finders by more than half a slot
   (about 16 pixels against slots of 13 at 4K with k1 = 0.04), so the homography is fitted after
   radial distortion about the capture's centre, k1 r^2 + k2 r^4 (step 3). The lens is searched
   outwards from no distortion, in steps of 0.02 over [-0.1, 0.1]: k1 with k2 = 0, then k2 with the
   k1 of the highest slot contrast, then k1 again at a quarter and half a step either side; the
   first lens through which the header decodes is taken, and when none does, the one of the
   highest contrast. Preferring the smallest distortion that reads the header matters on damaged
   headers: with two copies blank, a strong k2 bent their slots into data cells and raised the
   contrast above the true lens's. A finder's black and white come from its centre column (ring 0
   and 1 dark, ring 2 light; inverted for the fourth finder), where a module is 1/126 of the finder
   rectangle whatever the grid, measured through the model without a lens (at the finders the
   homography alone is exact, and a lens candidate must not move its own references). The
   three-finder search uses the k1 stage only. Each lens candidate, up to 25, is decoded, which
   widens the exposure to a false header; it is bounded by the header's CRC-16, its validation
   and the eight-anchor confirmation, and the hostile and decoy studies saw no wrong header. Pairs are tried among the copies that did not decode
   alone, and all four only when nothing decoded.
3. **Fit the mesh.** Locate every anchor's checker corner to sub-pixel accuracy, reject outliers, and
   interpolate. This absorbs lens distortion, rolling-shutter shear and stabilisation warping.
   As built: anchors are visited from the finders outwards (at first the finders are the only
   measured points, so prediction is best next to them). Each saddle is predicted from its located
   neighbours on the anchor lattice (2 P1 - P2 along a row or column, or the parallelogram of a
   diagonal), which does not depend on the shape of the lens distortion; with no located
   neighbours, by the model of step 2 plus the displacement of the anchors located nearby. It is
   found by the strongest saddle response within 1.5 cells and refined where the image gradient is
   perpendicular to the direction from the corner; a saddle found from its neighbours must also lie
   within 3 cells of the global model (twice the search radius), a coarser consistency check. The
   global model is refitted to the located anchors at 12 and each time their number doubles. Its
   lens is k1 r^2 + k2 r^4, each within +-0.1 (half capture widths; a statement about consumer
   lenses corrected in the camera, which leave a few per cent at the frame's edge, not a fit to a
   recording). The supported range is that box minus the region where the lens folds the image
   before the frame's corner (1 + 3 k1 r^2 + 5 k2 r^4 <= 0 there, near k1 = k2 = -0.1), which
   the simulator refuses. The S2 recordings motivated k2: on both phones the anchors fitted k1 near -0.07 and
   k2 near +0.06 ("moustache" distortion), 0.2-0.9 pixel RMS, against 0.5-2.2 with k1 alone, whose
   error at the code's left edge put the anchors there outside the search. k1, k2 and the
   homography are estimated together by iterated linear least squares (searching one term at a time
   stalls: over the points the two nearly cancel), and k2 is kept only when the Bayesian information
   criterion favours it over k1 alone (N ln(rss1 / rss2) > ln N, N coordinates); without that, k2
   follows the points' noise and extrapolates worse. The model is refitted to the corners, and a
   corner more than 0.35 cell from its neighbours' median displacement is rejected. The mesh's
   nodes, every 4 cells, are the model plus the corners' residuals interpolated over the anchor
   lattice; finders within half a module of where the anchors alone put them (step 1's rule) are
   measured points of that interpolation too, setting the unknown nodes of the lattice square that
   holds them, so the cells outside the first and last anchor column and row lie between finder and
   anchor rather than beyond the anchors (simulated moustache lens, 4K handheld: 0.0179 to 0.0161
   cell RMS in those columns, 0.0144 elsewhere); cell (x, y) covers
   [x, x+1) x [y, y+1) of the mesh's coordinates. The fit fails unless at least one anchor, and at
   least a quarter of those the model places inside the capture, are located. Once eight anchors are
   located, a finder stays in the global model's fit only while it lies within a quarter cell of
   where the anchors alone put it: a finder half a module to a cell off had pulled the mesh corner
   by up to 0.5 cell with the weight of an anchor, now 0.00 (sampled every third cell, 240 x 135 and
   212 x 120 grids), while accurate finders keep steadying the corners (dropping them all raised
   the cell-centre error at 4.0 pixels per cell by about 7%).
4. **Sample.** Take the mean of the central 60% of each cell's footprint directly from the camera planes.
   The image is never warped whole. As built: a cell is saturated when any of its brightness samples is
   at the bottom or top of the code range; a cell outside the capture is saturated with zero values.
   **Glare.** In limited-range video a sensor-clipped highlight lands near the nominal white (235 at
   8 bits), not the top of the code range, so a cell is also saturated when it is under glare. A
   cell is clipped when at least half of its samples are at or above 231 (the nominal white less 4
   codes; a clipped sample falls below 231 about one time in twenty under the handheld noise, while
   display white exposed at 0.9 of the range, about 225, rarely reaches it). A clipped cell is
   flagged when 90% of the samples over its 3 x 3-cell neighbourhood are clipped too (inside a
   patch), or when at least two of its eight neighbours are such cells (a patch's rim). The decision
   is local: a white data cell at the clip stands among scrambled cells, so it is spared whether the
   display's white reaches the clip over the whole code or over a part of it (an earlier rule
   decided that for the whole capture, and flagged up to 4.8% of cells when the clip covered only
   part of the code). Measured on the simulator's 5% patch (handheld, 4K): 2.4% of the cells it
   turns near-white unflagged, no untouched cell flagged; on the ideal preset 0.00% (four greys) and
   0.3% (black and white) of cells flagged; with white at the clip over part of a rendered code, at
   most 1.7% (black and white) and 0.04% (four greys). **Accepted cost:** on a perfect digital
   frame, whose white is exactly the limited-range white, the rule discards 1.46-2.80% of
   black-and-white cells, mean about 2.1% (white cells among mostly white neighbours; measured over
   seeds 30 to 39 on 212 x 120, 240 x 135 and 480 x 270 grids), at most 0.04% with four greys and none
   with eight colours or levels. The tests allow 3.5% and 0.1% (designer ruling, 2026-11-15: the
   earlier 2% ceiling came from one seed); M3 checks that the digital
   loopback still passes with margin under that loss (section 18). Cells on a patch's soft edge,
   brightened but under the clip, are not flagged here; the equaliser compares them with the
   learned white and the neighbouring tiles (steps 5 and 6). Anchors' Black and White are not
   flagged by this rule.
5. **Calibrate levels.** Per tile of about 32 x 30 cells, keep a centre and variance for each level.
   Start from the palette and the anchors' black and white, refine with two or three rounds of
   expectation-maximisation on the data cells (scrambling makes levels equally likely, and their order
   resolves the labels), and interpolate between tiles. Only cells that the anchors' phase field counts
   are fitted: in M2 the blends of two frames widened the variances, the dark level's most (a blend
   adds far more to a dark cell's code value than it takes from a light one's), and soft bits of clean
   cells came out up to 18 times less confident than they were.
   The equaliser then makes unknown (zero soft bits) the cells that need the learned levels to judge:
   a cell brighter than its tile's lightest level by 5 standard deviations of that level (at least 3
   code values), or one among the 5 x 5 cells around which so many read as the lightest level that
   chance would allow it less than once in a million, as a glare patch makes them; the flagged region
   grows by two rings for a patch's soft edge. Chance is one cell in L, or the median over the tiles
   of their share of cells that read as the lightest level when that is at least 1.5 in L: an
   overexposed capture that clips more than the lightest level makes those levels read lightest
   everywhere, and the rule then erased cells that did not clip (Gray4: 3.9% of them with two levels
   clipped, 93% with three, on a bench where the palette did not clip; under the simulator's own
   overexposure, which clips the palette too, the rule before and after this gives identical
   results); a patch moves few tiles' shares. A known limit: with three of four levels clipped,
   chance is 0.75 and a patch is no longer found. What its cells then carry is little: through the
   geometry pipeline 2 of 2,329 cells under the patch kept soft bits and none of their bits was
   wrong; on the bench every soft bit came out at the lowest confidence (|s| at most 11). Such a
   frame is lost, cleanly. Cells the phase field does not credit never trip the
   brightness margin: they had no stripes removed, and a residual fringe lifted them above white (the M2
   review found 0.1% to 0.2% of credited cells lost that way at fringe amplitude 24). Measured in M2
   with a patch over 5% of the code, counting credited cells only: of those it lifts by more than half a
   level spacing, 0% to 4.6% still give soft bits; of those more than four cells from it, at most 0.007%
   are lost; fringes of amplitude 24 without a patch lose 0.02% of credited cells. With glare and fringes
   of amplitude 24 together, 2.75% (`tripod`) and 0.05% (`handheld`) of the lifted cells keep soft bits,
   bounded at 5% (they were 6% and 26% before the sampler and the rule counted clipped cells, PRs #38
   and #31), and 0.1% to 0.5% of the distant cells are lost, a residual: stripe cancellation answers
   the patch's broad bump with waves across the capture. Through the geometry pipeline the sampler and the equaliser together
   miss 0.037 to 0.038 of the cells under the patch on `tripod` and 0.012 to 0.013 on `handheld`,
   bounded at 0.06 on both (designer's decision of 2026-11-16). That needs the rule to count the cells
   the sampler marks unknown because they clipped (PR #38) among the neighbours that read white: the
   sampler's flag had hidden the patch's core from the rule, and the `tripod` miss rate rose to 0.138
   until it did. Before PR #38 the pipeline missed 0.073 to 0.076 on `tripod`. A missed glare cell is wrong data stated with confidence. The share of a
   cell's footprint near the top of the code range belongs to the sampler.
6. **Cancel stripes.** Subtract each cell's decided level, take a 2-D Fourier transform of what remains,
   remove peaks standing above the noise, and re-estimate. Moire and backlight flicker are a few plane
   waves and show up as such peaks. As built (M2): the cell grid is padded to the next sizes with no prime
   factor above 5, so gaps stay near a tenth of it; a peak is a bin above 20 times the median power, its
   neighbours going with it above 4 times; the estimate is repeated on what is left up to six times with
   the levels decided afresh, and the whole step twice with the levels refitted between.
7. **Soft bits.** For each bit, the difference between the best matching level with that bit 0 and with
   that bit 1, scaled by the variance, as a clipped 8-bit value in units of a quarter nat
   (`modem.LLRScale`). Saturated cells give zero. A cell with phase weight *w* has its variances widened
   by *u*^2 times the variance of the level centres, per plane used and averaged over those planes (Y
   alone for grey modulations), where *u* = 1 - *w* is the share of another frame the cell may hold.
   Measured in M2 on the credited cells that hold some of another frame, pooled over the variants: with
   this size `handheld` is honest (Gray4 bucket 1: 0.0079 observed where 0.0116 is stated); a third of
   it, as first built, left `tripod`'s partly mixed cells up to 9 times overconfident. Three residuals
   remain, accepted by the designer on 2026-11-15 and revisited with the S2 recordings:
   - **`tripod` partly mixed cells are overconfident** in buckets 0 to 2, because the phase field
     overestimates the weight of cells beside narrow blends. At true cell positions (Gray4): 1.7, 4.3
     and 2.7 times the stated rate, about 1% of credited bits. Through the geometry pipeline (detector,
     mesh and sampler), on captures with more mixing, pooled bucket 1 is 9.76 times for the partly
     mixed cells and 8.69 times for the whole bucket at CI size (4.6 times in partly mixed bucket 2),
     and 11.08 and 10.25 times in the long version: a margin of only 1.08 under the bound of 12, to be
     revisited first with S2. One bounded attempt to cap such a cell's soft bits at
     ln(*w* / (1 - *w*)) + 1 nat made them underconfident instead (0.43 and 0.005 times in bucket 1)
     and was reverted. The honesty tests bound the overconfidence at 12 times on `tripod`.
   - **The partly mixed cells cost the decoder** near its margin: through the pipeline, on `tripod`
     at sensor noise 16, scaling their soft bits by 0.6 decodes 1,490 of 1,506 codewords against 1,480
     as produced, about 0.7% of codewords (none at noise 14 and 15). An earlier check at true
     positions and easier settings found no cost; it no longer stands. `TestPipelineMixedCost` bounds
     the cost at 15 codewords.
   - **Clean cells are underconfident**: on `tripod` at true positions (Gray4 bucket 1: 24 errors
     where 167 are expected, 0.14 times; bucket 2: none where 23 are expected), probably because the
     credited but partly mixed cells widen the fitted variances, and down to 0.035 times in the long
     version; on `handheld` down to 0.13 times at true positions and 0.02 times through the pipeline
     (long versions). Underconfidence is the safe direction for the LDPC decoder, but it is lost
     capacity; no lower edge is asserted (designer's ruling of 2026-11-15), and calibrating it is a
     likely gain to look for in the S2 analysis.
   - **Open (S2 interim, 2026-11-17).** In the simulator at 1080p30 on `tripod` (Gray4, 4-pixel cells,
     12 fps display; spikes/pattern `TestAnalyseSimulated`), partly mixed cells are 26 and 74 times
     overconfident, in buckets of 2,850 and 8,871 bits. Those buckets are too small to be judged at
     10,000 bits, so CI does not catch it. The task is radiometry on real data, after the S2 re-run
     (docs/spikes/S2.md); no work on it before then.
   M2 settled the two points the M1 review left open. The omitted log-variance term, (planes / 2) times
   the log of the ratio of two levels' variances, measured under `handheld` up to 1.43 nats for Gray4
   (per-tile variance ratio 17.4, the dark level widest) and 3.41 nats for RGB8 (ratio 9.7, three planes),
   yet confidence measured honest without it (section 15), and `modem.LLR` is unchanged. The 1/12 minimum variance never binds: a
   soft bit is clipped once half the level spacing exceeds 8 times the standard deviation, 2.3 code
   values at that floor, and the narrowest half-spacing of any modulation is 15.6 (Gray8, on the 8-bit
   scale), so the floor costs nothing at 10 bits either.
8. **Assign and sum.** Apply the phase rules of section 7 and add into the code frame's accumulator. The
   phase field is estimated from the anchors alone; estimating per-cell weights from the cells' own
   observations near a boundary is not built (owner's decision of 2026-11-15, section 15).

| Distortion | Handled by |
| --- | --- |
| Perspective, rotation | Finders and homography (the capture is assumed not to be mirrored) |
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

**As built (M3, `internal/pipeline`).** Recordings are read one after another, each by one video
decoder whose captures go to a pool of vision workers (one per core, as many as half the decode's
memory budget holds for the probed capture size, each with its own detector and buffers; the grid
stage, from sampling to soft bits, is admitted by a gate sized from the grid, section 19); the workers return each capture's soft bits, descrambled and in codeword order, through a
channel to the one goroutine that owns the accumulators, the shard store and the events, which
applies them in capture order. A phase of the capture is credited to the header's frame with that
phase, else to the frame one step after or before the first header's (wrapping at the seam); two
steps away is skipped (section 7, rule 1 as the S2 analysis did it). The decode locks onto the
first located capture's session, grid, modulation, inner code and frame count; other captures are
counted and ignored (SECURITY.md T2). Soft bits are summed in 8 bits, saturating; a capture of
the frame the previous capture of the same recording also credited adds half (rule 7). Codeword
0 of every frame is summed into one descriptor accumulator, tried after each capture until it
decodes and validates; then the plan and the shard store are made. A frame is attempted once two
captures have passed without it, located or not, and at each recording's end (rule 11). A capture
is read only while fewer than workers + 2 results wait to be applied, so a stalled worker holds
back the reading. Before the shard store is made, the temporary folder's free space less 1 GiB
must hold the store and the stream, and the stream again before it is written (SECURITY.md
section 5 and T3; otherwise a `LimitError`). Reading stops as soon as
every block holds K shards. Beyond `MaxSoftBytes` (256 MB) the frame with the least mean confidence
loses its soft bits first. The stream is then reconstructed block by block, its hash checked, and
`internal/payload` verifies and unpacks it. `--state-dir` is not built yet (`ErrNotImplemented`;
M6). The encoder renders runs of an even number of frames on one worker each, every run to its own
H.264 elementary stream, joined by `Concat`; parity shards are kept in a file of their own beside
the spooled stream.

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
   handheld and 0.90 on a tripod. For four greys under the handheld blur (Gaussian sigma 1.5 camera
   pixels, applied in linear light before the camera's tone curve), the minimum is 4.8 for `robust`
   and 6 for `normal` (re-derived on 2026-11-18, simulated). For `fast` it is **not established**:
   - The simulator decodes 99.6% of `fast` codewords at 7.25.
   - S2's real four-grey segments give 0.815 and 0.837 bits per sent bit at 7.2-7.8 camera pixels
     per cell, below the about 0.89 `fast` needs, then 0.879 at 6.4 and 0.949 at 10.1. A segment at
     0.937 had only 27 of its 239 captures located, so it is selection-biased.
   - Planning uses 10 provisionally until the G1 recordings. The simulator's
   `budget-android` at 1080p decodes, per capture through the vision pipeline, every `robust`
   codeword at 4.8, and 9.5% of `normal` codewords at 4.8 against 99% at 5.4. It decodes 11% of
   `fast` codewords at 6.4 against 99.6% at 7.25. S2's real recordings decode `normal` from 6.4
   (docs/spikes/S2.md, section 1.4; its conservative setting is 6 or more). The more conservative
   figure is taken. On a tripod (sigma 1.0) the steady figure stays at 3.5 (section 4): `normal` was
   re-checked at 3.6, with 99.7% of codewords decoding, and `fast` and `robust` were not
   re-derived. Earlier this condition said 4.5 for
   four greys handheld, from section 4's simulation, which blurred code values and modelled neither
   gamma nor sharpening; with blur in linear light the same model's symbol error rate at 4.8 pixels
   per cell is 0.21 instead of 0.10. Geometry alone (finders, header, mesh; section 10 step 1, measured
   on the simulator with a finder module of one cell) is reliable from 4.0 camera pixels per cell
   under the handheld blur (3.6 for budget-android) and 4.4 under the 10-bit HLG preset, so the
   planning point of 5 leaves a margin of about 1.25 and 1.1 times; at 5 the cell-centre error is
   about 0.05 cell RMS.
3. Recorder bits per cell per code frame `q = 0.9 x bitrate / (rate x cells)` at least 9 for `fast`,
   5.5 for `normal`, 2.5 for `robust` with HEVC; 1.4 times those with H.264.
4. The display video's bitrate, `8.4 x cells x rate`, stays playable: 33 Mbit/s for 1080p30,
   58 for 1440p30, 131 for 4K30.

Planning figures are the simulated single-pass rate derated by 40% for everything not simulated. All
of them are simulated. The main figures are **handheld** (fill 0.80, the four-grey condition of
condition 2: `robust` 4.8, `normal` 6, `fast` 10 provisionally). Each row takes the highest-rate
setting that conditions 1 to 3 allow. A planning row may use a setting only if the simulator and real
recordings both support it, wherever real recordings exist for that modulation; otherwise the setting
is labelled not established and is not used for planning.

**Black and white at 4.8 camera pixels per cell.** `normal` is established: real recordings reach the
0.78 bits per sent bit it needs from 3.0. `fast` is not established, and is to be settled at G1:
- The simulator decoded 513 of 513 `fast` codewords at 4.8.
- S2's real black-and-white segments with the camera at least twice the display rate give, in bits per
  sent bit against the about 0.89 `fast` needs: 0.858 at 3.0 camera pixels per cell; 0.862 at 3.6
  (92 of 324 captures located); 0.562 at 4.0 (5 of 178 located); 0.705 at 4.0 (48 of 348
  located); and 0.975 at 6.4.
- There are none at 4.8.

So the 1080p30 row uses black and white `normal`, which is also the always-on CI row for this camera
class.

| Camera (handheld) | Recommended | s | Planning rate | 10 MB | 1 GB |
| --- | --- | --- | --- | --- | --- |
| 1080p30 (older Android, webcam) | 720p at 12 fps, black and white, `normal` | 4.8 | about 25 KB/s | about 7 min | about 11 h, impractical |
| 4K30 | 1080p at 12 fps, four greys, `normal` | 6.4 | about 112 KB/s | about 1.5 min | about 2.5 h |
| 4K60 (with a 1080p or larger monitor) | 1080p at 30 fps, four greys, `normal` | 6.4 | about 280 KB/s | about 36 s | about 1 h |
| 4K120 | 1080p at 60 fps, four greys, `normal` | 6.4 | about 560 KB/s | about 18 s | about 30 min |

At a 4K camera, 1440p gives 4.8 camera pixels per cell handheld. That allows four greys `robust`, at
about 255 KB/s at 30 fps, or black and white `normal`, at about 251 KB/s; both are below 1080p
`normal`. Four greys `fast` is not used in any handheld row, since it needs 10 provisionally. Colour
`normal` is not chosen on the 4K rows, for two reasons: their q of 12-13 is below the about 15 above
which section 5 favours colour, and colour's camera-pixels-per-cell figure was never re-derived under
linear-light blur.

**On a tripod** (fill 0.90; the steady four-grey figure of 3.5 from section 4, with `normal`
re-checked at 3.6 and `fast` and `robust` not re-derived). This table is **simulated only and
indicative**:
- S2 has no tripod take.
- The handheld real recordings are a lower bound for tripod use and do not establish any tripod-only
  setting.
- It is not used for recommendations until a tripod take is filmed at G1.
- Four greys `fast` is not established on a tripod either, so no row uses it.

| Camera (tripod) | Recommended | s | Planning rate | 10 MB | 1 GB |
| --- | --- | --- | --- | --- | --- |
| 1080p30 | 720p at 12 fps, four greys, `normal` | 5.4 | about 50 KB/s | about 3.5 min | about 5.5 h, impractical |
| 4K30 | 1440p at 12 fps, four greys, `normal` | 5.4 | about 200 KB/s | about 50 s | about 80 min |
| 4K60 with a 1080p monitor | 1080p at 30 fps, four greys, `normal` | 7.2 | about 280 KB/s | about 37 s | about 1 h |
| 4K60 | 1440p at 30 fps, four greys, `normal` | 5.4 | about 500 KB/s | about 20 s | about 32 min |
| 4K120 | 1440p at 30-60 fps, four greys, `normal` | 5.4 | 0.6-1.0 MB/s | 10-16 s | 16-26 min |

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
  at 60 s of running time and 4 x the file's size + 16 MiB of reads (SECURITY.md section 5); probes of
  the S1 and S2 files took at most 1.2 s and read at most 1.07 times the file, but one probe under
  the full CI test load took 13 s, and the time bound is a backstop sized at about four times that.
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
  Nor is an instance stalled until its 512 MiB of linear memory has been reserved, which happens
  before any guest code runs: the stall clock starts at the reservation, on this path and in a
  worker, and `video.EngineLoadLimit` bounds the time from the instance's turn to it (the instance is
  cancelled after 10 minutes). Two reservations took 30 s on macos-26-intel and were stopped as
  stalls (CI run 43693218308; the cause there was not established, and parallel reservations under a
  memory limit reproduce it locally).
  Encoding and `Concat` instances are not stopped by the watchdog (below). At most GOMAXPROCS - 1 instances run at once
  (`video.EngineInstances()`, at least one), and an instance waiting for its turn is not stalled. The
  cap was added after a 30 s stall on the 4-vCPU CI runners stopped after 79 s, reasoning that compiled
  Wasm cannot be preempted. That reasoning does not hold while close-on-cancel is on: wazero then
  returns to Go at every loop back-edge for its check (done in Go so that it can be preempted,
  `call_engine.go`, `ExitCodeCheckModuleExitCode`), and without the cap the watchdog still stops
  every instance, but not promptly: uncapped, stops were up to 8 times late on a CI runner
  (macos-26-intel, GOMAXPROCS + 1 spinning instances, 16.8 s against a 2 s limit), while the capped
  run stayed within its bound; on a 16-thread development machine GOMAXPROCS + 2 uncapped instances
  were stopped on time (`TestWatchdogUnderLoad` asserts, in the capped case, that each instance is cancelled at most 5 s after its memory reservation plus the limit, with or without the race detector, and that every one stops; it logs the time from each turn to its reservation and how long each took to return after its cancellation, and logs the uncapped case. The time from the turn to the reservation is bounded separately by `video.EngineLoadLimit` (10 minutes): `TestReservationLoadLimit` asserts that an instance still reserving when the limit runs out is cancelled with that limit's error and its slot is released. A hostile file cannot stretch that phase, because no guest code runs and no input is read before the reservation. The test counted from the turn until main's push run at df8e9b5 (CI run 44400775718) failed it on macos-26-intel at 6.0 s against 5 s, where the reservation is inferred to have taken 5 to 6 s; section 19).
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
  messages (output, log, progress, exit status; the progress message carries the bytes counted as a
  probe's progress, the input's bytes and read calls, and the time from readiness to the first read,
  to the linear memory being reserved and to the input being opened, for the breakdown a time-bound
  error carries, SECURITY.md section 5) that the parent bounds before allocating and sanitises
  before printing; a lying worker can end a job, not make the parent allocate. The worker
  gets an empty environment (on Windows only `SYSTEMROOT`, `TEMP`, `TMP`), its working directory in the
  temporary directory, and no descriptors or handles but its three standard ones: on Unix it closes,
  as it starts, every descriptor above 2 without close-on-exec (any one qrvt itself inherited, which
  exec passes on; Go opens its own with the flag), and on Windows Go passes a child only the handles
  it names. It exits by itself the moment its
  standard input closes, which happens when the parent exits or dies, and runs with GOMAXPROCS of at
  least 2 so that the goroutine watching the pipe can run while the engine occupies a core; on Windows
  it is also placed in a kill-on-close Job Object: it is created suspended and resumed only once it is
  in the job, so it never runs outside it (if it cannot be assigned it runs without one); on Linux it
  is started with SIGKILL as its parent-death signal, from a thread held until it has been waited
  for (the signal fires when the starting thread exits). That exit is best effort: with close-on-cancel off
  a spinning guest never returns to Go, and it also blocks Go's stop-the-world garbage collection, so
  while it spins the worker's own timers and goroutines cannot be trusted. The parent's kill is the
  control. Loading and compiling the engine, which on a first run
  of a slow machine takes longer than 30 s, is not a stall, nor is reserving the instance's 512 MiB:
  the worker sends a ready message once the engine is loaded and reports the reservation in a
  progress message sent by the goroutine that made it, both before any guest code runs (a periodic
  report could be held back for good: once the guest spins, a garbage collection the reservation
  started stops every other goroutine of the worker), and the parent never takes the reported time
  as later than the message's arrival; the watchdog counts from the reservation, and
  the phase from the worker's start to it is bounded by `video.EngineLoadLimit`, 10 minutes, after
  which the worker is killed.
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
| `--max-entries` | decode | 100,000 (`DefaultMaxEntries`), with a manifest of at most 6,710,886 bytes (`DefaultMaxManifestBytes`, 64 MiB / 10) | Largest number of manifest entries that may be unpacked, 1-1,000,000; the manifest may then hold N x 64 MiB / 1,000,000 bytes, rounded down, at most 64 MiB. A manifest over either limit fails with guidance naming this flag, unless the limits are already the format limits, which nothing raises. The size limit usually binds before the entry limit: a manifest entry takes about 150 bytes plus its path, so about 40,000 typical entries fit at the defaults (the widest manifest measured at them, empty files with names of one to four characters, held 43,026). The flag reaches extraction in M3, through the pipeline, which passes it to `internal/payload` as `UnpackOptions.MaxEntries` |
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
    MaxEntries       int      // manifest entries that may be unpacked (flag --max-entries); 0: DefaultMaxEntries (100,000) and DefaultMaxManifestBytes (6,710,886); N: N x 64 MiB / 1,000,000 bytes of manifest, rounded down; at most 1,000,000 and 64 MiB
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
| `EncodeConfig`, `DecodeConfig` | video | Size, rate, quantiser, bit depth, and a bitrate (M3, for the simulated recorder only; 0 keeps the constant quantiser, and the public API does not reach it; refused with a quantiser and by the raw-frame backend; its MP4 is written like the display video's, without an edit list, since the recorder uses no B-frames, section 15); time slice and brightness-only for decoding |
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
   pure-Go quantiser stand-in. x264 stands in for the phone's HEVC recorder: an approximation of a
   real phone, not a measurement of one. As built (M3): `video.EncodeConfig.Bitrate` selects x264's
   defaults at `veryfast`, a GOP of one second at the camera's rate, single-pass average bitrate
   capped at the bitrate with a two-second buffer, BT.709 tags, High 10 for 10-bit captures
   (`TestRoundTripMatrixX264`), except two settings: no lookahead and no B-frames
   (`rc-lookahead=0:bframes=0`). Those are forced by the engine's 512 MiB memory bound
   (SECURITY.md section 5), and were chosen before any row's result at 4K was known (designer's
   ruling, 2026-11-18): x264's defaults could not encode a 4K capture inside it. Measured at
   3840 x 2160, 30 frames at 74 Mbit/s:

   | x264 settings | 8-bit | 10-bit |
   | --- | --- | --- |
   | defaults (`veryfast`: lookahead 10, 3 B-frames) | fails at frame 13 (an allocation of 44,008,576 bytes) | fails at frame 6 (48,303,424) |
   | `rc-lookahead=5` | fails | fails |
   | `rc-lookahead=5:bframes=1` | fits | fails |
   | `rc-lookahead=10:bframes=0` | fits | fails |
   | `rc-lookahead=0:bframes=0` | fits | fits |

   Without B-frames the MP4 needs no edit list, so the recorder writes the display video's
   container; with them, and no edit list, the reader lost a clip's first frame.

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
  frame width, to the pinhole point p (1 + k1 |p|^2 + k2 |p|^4); positive k1 is barrel. The presets
  have k2 = 0. The tests' moustache variant (k1 = -0.05, k2 = +0.05: -1.2% at r = 0.7, +2.1% in a
  16:9 corner) is a plausible corrected phone lens; the S2 recordings motivated adding the term,
  and the simulator stays uncalibrated.
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
| Gamma, white balance, tone curve, vignetting | Confidence is honest: with soft bits grouped into ten buckets by stated confidence, the observed error rate of every bucket holding at least 10,000 bits is at most twice the stated rate. A bucket passes when its error count is consistent, at the 0.1% level of a Poisson test, with that rate; in the tables pooled over the variants, wherever at least 100 errors are expected, the observed rate itself must also be at most twice the stated one. Credited cells that are wholly one frame and those that hold some of another are also pooled and judged apart, so that errors in opposite directions cannot cancel. Only overconfidence is asserted: by the designer's ruling of 2026-11-15 no lower edge is asserted anywhere, at true positions or through the pipeline, and underconfidence is logged. Met in M2 on `tripod` and `handheld` for Gray4, Gray2 and RGB8 under gamma error +-0.3, white balance +-0.10 and vignetting 0.25, each case alone and pooled, at true cell positions; and through the geometry pipeline (detector, mesh fitter and sampler; Gray4, 480 x 270 grid, 4K cameras; the preset and two variants, gamma error +0.3 and white balance -0.10, in CI, the preset and five variants in the long version). The one exception is `tripod`'s partly mixed cells, bounded at 12 times (through the pipeline, pooled bucket 1: 9.76 times for the partly mixed cells and 8.69 for the whole table at CI size, 11.08 and 10.25 in the long version, a margin of only 1.08; an open residual for S2, section 10 step 7). Underconfidence measured in the long versions (480 x 270 grid, 4K cameras): `handheld` down to 0.13 times the stated rate (Gray4 pooled bucket 2: 31 errors where 239 are expected; RGB8 down to 0.10 times in one case), `tripod`'s clean cells down to 0.035 times (Gray4 pooled bucket 1: 4 errors where 115 are expected), and through the pipeline `handheld` down to 0.02 times. On clean captures the soft bits are therefore several times more cautious than the measured error rate justifies, which costs capacity: the LDPC decoder is given less confidence than it could use. Calibrating it is a likely gain to look for in the S2 analysis, where real noise replaces the simulator's |
| Moire fringes, flicker bands | Decoding succeeds with stripe cancellation on at an amplitude where it fails with it off. Met in M2: on `handheld`, Gray4 with `normal`, at fringe amplitude 24 every codeword decodes with it on and none with it off, for three fringe periods and directions |
| Frame mixing, any boundary direction | No soft bit is ever credited to the wrong code frame; every credited cell's true weight is at least 0.80; per preset, the share of truly clean cells (true weight at least 0.92) that are credited is at least its measured value less 0.02 and never under 0.96 (M2, CI and long version: `tripod` 0.9843 and 0.9863, floors 0.964 and 0.966; `handheld` 0.9930 and 0.9904, floors 0.973 and 0.970); as a regression check, the phase weight error is at most 0.09 at cells interpolated between four observed anchors and 0.16 beyond the outermost anchors and beside the finders. CI runs `handheld` with the panel scanning left to right at roll 60, which reaches 0.082 inner error; the smallest credited true weight, 0.802, and the pocket error closest to its bound, 0.149 against 0.16 (the tightest margin), occur only in the long run. The cost of this accepted phase accuracy is the clean-cell yield: of the cells that truly show one frame (weight at least 0.92), 0.99 are credited on `handheld` (0.9930 CI, 0.9904 long) and 0.986 on `tripod` (0.9843 CI, 0.9863 long); the designer accepted it on 2026-11-15. These replace "phase weight error at most 0.05" by the owner's decision of 2026-11-15, after five estimators were measured on `tripod` and `handheld` (largest error, inner / pockets): weights divided by the anchor's white with bilinear interpolation and nearest-neighbour fill, 0.13 / 0.44 on a 212 x 120 grid; the same with linear prediction of missing anchors, 0.07 / 0.15 on the 480 x 270 grid with 4K cameras; bicubic Catmull-Rom interpolation, no gain and worse at a sharp ramp's kinks; weights divided by the quadrants' own total, as built, 0.082 / 0.149; a boundary model fitted to all anchors at once (a quadratic surface of display time and the blur of exposure and panel response), 0.01-0.04 where the anchors see the blend but 0.067 / 0.15 where it lies beyond them, not adopted. None credited a cell to the wrong frame. The residual error comes from blends the anchors cannot locate (opposite scan directions, blends beyond the outermost anchors); per-cell estimation from the cells' own observations is not built, and the question is revisited in M5 only if the S2 or G1 recordings show that the cells it discards matter. Through the geometry pipeline (Gray4, 480 x 270 grid, 4K cameras; the long version all directions at rolls 0, 30 and 60, 191 captures, every one found and fitted): no wrong credit; the smallest credited true weight 0.8668 (CI) and 0.8018 (long); yield 0.9730 and 0.9864 on `tripod`, 0.9975 and 0.9894 on `handheld`, the CI `tripod` figure 0.009 above its floor of 0.964 (0.013 above 0.96); the phase weight error up to 0.058 inner and 0.139 in the pockets in CI, and 0.1105 and 0.1724 in the long version, set by `handheld` scanning left to right at roll 60, where the rectifier's anchors are less exact than the true positions. The pipeline's regression bounds are therefore 0.12 inner and 0.19 in the pockets, beside the 0.09 and 0.16 at true positions; the owner's floors (no wrong credit, credited weight at least 0.80, yield at least 0.96) are unchanged. The weight floor is met with almost no margin: 0.8018 against 0.80 in the long version, on one pose, both at true positions and through the pipeline. It passes, but not comfortably, and S2 checks it on real captures |
| Camera at twice the display rate, any phase offset | One pass succeeds for every offset |
| Camera equal to the display rate | At 30 fps both, 4 ms exposure and 8 ms readout: succeeds within 4 loop passes |
| Dropped frames, bursts, mid-loop start | With parity 20, whose budget is 16.7% of frames: succeeds at 14.7% random loss; at 18.7% reports which blocks are short and writes nothing |
| Glare patches, overlays | `normal` succeeds with a patch over 5% of the code; `robust` with 20%. M2 measured the cells under a patch over 5% of the code that keep soft bits (section 10, step 5): at true positions 0% to 4.6%, bounded at 0.05; through the geometry pipeline, where the sampler's clipping flag and the equaliser's rule act together, 0.037 to 0.038 on `tripod` and 0.012 to 0.013 on `handheld`, bounded at 0.06 |
| Two partial recordings | Each alone fails; together they succeed |
| Whole system | SHA-256 round trip for every row of the matrix below; identical raw frame hashes on every platform |

Round-trip matrix. `--hdr` joins it in M6.

| Preset | Video | Modes | Protection |
| --- | --- | --- | --- |
| `ideal` | 480p at 12 fps | bw, gray4, color | fast, normal, robust |
| `tripod` | 1080p at 30 fps | gray4 | fast, normal |
| `tripod` | 1080p at 30 fps | color | normal |
| `handheld` | 1080p at 30 fps | bw, gray4 | normal, robust |
| `budget-android` | 720p at 12 fps | bw | normal, robust |
| `budget-android` | 720p at 12 fps | gray4 | robust |
| `iphone-hdr` | 1080p at 30 fps | gray4 | normal |
| `harsh` | 720p at 12 fps | bw | robust |

These thresholds and this matrix are untested estimates. A cell that fails is a defect unless the
feasibility conditions of section 11 say it should not have passed, in which case it is removed with a
note in this document.

`budget-android` at 720p, four greys, `normal` was removed on 2026-11-18 as infeasible. It films 4.8
camera pixels per cell, against the 6 that section 11's condition 2 now requires, and its
recommended setting is black and white. `TestMatrixFeasible` checks every four-grey row
against that condition, at the preset's camera and, for the rows CI runs, at the short version's
camera.

`iphone-hdr` four greys `normal` is asserted in CI below the condition by design. At the short
version's 3008 x 1692 camera it films 5.0 camera pixels per cell, against the 6 required, and it
passes because the stand-in recorder gives two captures per code frame. It is the test's one
exemption (section 19). At the preset's 4K camera, 6.4, it meets the condition.

### Test videos

| Kind | Produced by | Use |
| --- | --- | --- |
| To film | `qrvt calibrate make` | Steps through every mode, density and display rate on a real screen |
| Synthetic "as filmed" | `tools/dev testvideos` | One clip per simulator preset and one per single impairment, deterministic; a small set is committed |
| Reference | Committed once per format version | Every later release must still decode it |

## 16. Scripts

| | macOS and Linux | Windows | Does |
| --- | --- | --- | --- |
| Setup | `scripts/setup.sh` | `scripts/setup.ps1` | Installs or verifies pinned Go from the official go.dev archive (SHA-256 pinned in the script), git through Homebrew, apt, dnf, pacman or winget, and the pinned development tools through `tools/dev install-tools`. Native ffmpeg and wasmtime (`--spike`), zig and qemu (`--cross`) are optional |
| Build | `scripts/build.sh` | `scripts/build.ps1` | Static build for the host, or all six targets |
| Test | `scripts/test.sh` | `scripts/test.ps1` | Unit and integration tests with the coverage gate; options for fuzzing, the simulator matrix and real recordings |

The setup scripts stand alone, because Go may not be installed yet: they install or verify Go itself and
then run `go run ./tools/dev install-tools`. Go comes from the go.dev release archive rather than a
package manager, because distributions lag and cannot pin a patch release; the archive's SHA-256 is
pinned in the script, and `GOTOOLCHAIN=local` stops an older Go from fetching a toolchain on its own.
With `--spike` they also install the two tools only spike S1 needs: a native ffmpeg with an HEVC encoder
(libx265, from the package manager or winget) and the wasmtime command-line runner (from its release
archive, SHA-256 pinned). With `--cross` (`-Cross` on Windows) they install what `tools/dev platforms`
needs: zig 0.17.1 from its release archive (SHA-256 pinned) and, on Linux, `qemu-user-static` from the
package manager; without root or sudo the script says that qemu is missing and goes on. They print
what they are about to install, and ask, before installing anything. The build and test scripts are thin wrappers around
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
  others. Every job and every long step has a timeout. A newer push to a pull request cancels that
  pull request's older `ci` and `codeql` runs; their runs on main, and codeql's weekly runs, have a
  group of their own (the commit SHA) and are never cancelled. The workflow policy test allows
  `cancel-in-progress` only as a bare `false` or the pull-request condition, in block or flow style,
  and with the latter requires a group holding both the pull request number and the commit SHA.
  **Documents-only pull requests**: a first `changes` job lists the files a pull request changes
  against its merge base, with their status (full history fetched; renames counted as a deletion
  and an addition). If every change adds or modifies a file on an allow-list of files that no test,
  build, script, workflow or release configuration reads (`tools/dev/changes.go`: `docs/FORMAT.md`,
  `docs/SECURITY.md`, `docs/IMPLEMENTATION_PROMPT.md`, the Markdown files directly in
  `docs/spikes/`, and the change-log entries in `docs/changes/<document>/`), the six-runner checks, release snapshot, setup scripts and short fuzzing are
  skipped; the workflow and licence checks and CodeQL still run. Every other event, every deletion,
  and any other changed file runs everything: code, tests, fixtures, golden files, build and release
  configuration, LICENSE, NOTICE, README, `docs/DESIGN.md` (whose preset tables `internal/channel`'s
  tests read), and any Go file, `go.mod`, `go.sum`, `.gitattributes` or `testdata/` path wherever it
  sits. Only a pull request into the default branch can skip: a stacked one, whose base is another
  branch, runs everything, because a later retarget to main would otherwise leave its green result
  standing for code never tested. That rule alone closes the retarget hole, so the `pull_request`
  trigger keeps its default types: with `edited`, every title or body edit would cost a full run. A
  test keeps the allow-list honest by failing if a Go string literal or `//go:embed` pattern reaches
  a listed file or the spike directory, if any Go file sits under `docs/`, or if a workflow, script
  or the GoReleaser configuration names a listed path. An aggregate `ci result` job waits for every
  job and fails on any failure or cancellation, on a `code` output other than `true` or `false`, or
  on a skipped job unless `code` is `false`; it is the check to require from `ci.yml`, because a
  skipped matrix job does not report under its expanded names. The test, race and coverage tasks run
  `internal/video` on its own, after the other packages have finished, not in parallel with them:
  the worker cap and the watchdog's stop latency are properties of one process on a machine that
  other test binaries are not oversubscribing, and the package's tests assert wall-clock bounds
  (`EngineProbeTime`, stall stops) that held alone and failed on 4-vCPU runners when the other
  packages' test binaries ran beside it. CI tests them under that condition (`tools/dev`,
  `isolatedRuns`). The cost, accepted by the owner, is longer coverage and race steps on the slowest
  runner (macos-26-intel); the figures first given here could not be tied to a run that retested the
  packages and are dropped.
  Under the race detector `internal/vision` runs a smoke subset: every code path and goroutine
  boundary, with one or two poses or captures each and its concurrency tests in full, while its
  statistical assertions run only in the non-race test step (`raceEnabled`, `raceSize` and
  `raceSkipStatistics` in its tests); its budget under race is about 3 minutes on a developer
  machine. With it, on macos-26-intel, `internal/vision` took 125 s under race, down from 959 s, which
  had pushed the race step past its timeout. In a run that retested every package (run 43774194055,
  no cache restored) the race step took 15 min 53 s, `internal/video` 528 s and `internal/vision`
  187 s; an earlier 13 min 26 s (run 43769482359) restored cached results for `internal/fec/conv`,
  `internal/fec/rs` and `internal/modem` and is not comparable. The race tests now run as two CI
  steps, each with its own 20-minute limit: every package but `internal/video`, the root package
  and `internal/pipeline` ("Race tests, other packages", `tools/dev test -race -part others`), then
  those three each alone ("Race tests, isolated packages", `-part isolated`, named so since PR #47
  moved the two end-to-end packages there), which runs even when the first step failed. Both pass `-count=1`: setup-go restores Go's test cache across runs, for every
  package unchanged since the cache was saved, and a cached race pass reuses one old run's luck for
  the nondeterministic tests (`internal/fec/conv`'s parallel test, `internal/fec/rs`'s subprocess
  test). That costs 0-3 minutes per run on macos-26-intel, nothing when nothing would have been
  cached. The `check` job's limit is 70 minutes, two race steps at 20 each plus 30 for every other
  step (`tools/dev` `fixedStepsBudget`, checked by a test against all four race steps): the split's
  own arithmetic, not an allowance for slow hosts. The 30 comes from macos-26-intel, the slowest
  runner: its non-race steps took 21 min 55 s in run 43811766030 and 20 min 13 s in run 43958754789
  (three packages restored from the cache). The earlier budget of 20 was exceeded in both runs, and
  trimming vision's always-on statistics alone did not bring the steps back under it; 30 leaves
  roughly 8 to 10 minutes of margin. Earlier, run 43797784643, with no cache restored, took 16 min
  54 s; the slow host of run 43774194055 took 23 min 09 s, and a host that slow is a runner fault
  (spike S1, section 14), for which the limits are not sized. The isolated packages' race step does not run alone after an
  earlier failure skipped the other packages' step.
  The detector's hostile-cost tests (`TestDetectThreeHostile` 16 times, `TestDetectThreeHostile4K`
  and `TestDetectHostileGrid4K` 6, `TestDetectHostileGrid` 2; SECURITY.md section 9.1) bound a
  ratio to a real code's Detect in the same process and run serially. The ratio is the median, over
  15 hostile runs, of each run's time against the mean of the real-code runs either side of it. It
  was the ratio of the two kinds' fastest runs until CI run 43967374482, where macos-26-intel's
  coverage step read 16.7 times under load: the short real-code run finds quiet moments the long
  hostile one does not. The estimator changed, the bound did not. Quiet, on one machine, the noise
  case reads 12.6 times (12.9 at most) by either estimator; when the bound was set it read 11.1, and
  the rise came from #38's three-candidate path (the hostile Detect from 260 to 295 ms, the real
  one unchanged at 23 ms). **Residual for the security pass (M7):** the 16-times bound has about 24%
  of quiet headroom (16 against 12.9); any detector change that adds hostile cost re-measures it.
  A step of the `check` job ("Hostile-cost ratios") runs the four tests verbosely and keeps
  their ratio lines, so that every runner's figures, a near miss included, can be read from the job
  log on a pass too. Since the vision test trim (2026-11-18) that step, without coverage, is where
  CI asserts them on every runner: they skip under the race detector and in a coverage build
  (`geomCostSkip`), whose instrumentation slows the two runs of a pair unevenly (pinned to 4 CPUs
  the noise case read 8.52 and 8.61 times under coverage against 8.99 and 8.92 without), and they
  still run in every plain `go test`, the untagged step and `tools/dev test` included. The release
  build tags select no different file in `internal/vision` or any package it imports, so the
  untagged step measures the shipped detector.
  **`internal/vision` in the coverage step.** The coverage counters of the code under test are
  shared memory, and goroutines on several cores writing them at once contend for their cache
  lines: pinned to 4 CPUs, `TestGeometryPresetsSmall` took 57 CPU seconds (13.7 s) under coverage
  against 15 (3.7 s) without, and `TestDetectConcurrent`, run alone, 34 CPU seconds against 2.3.
  So in a coverage build the
  pose sweeps (`geomSweep`, `geomDetectRate`) film their poses in parallel, the simulator not being
  instrumented, and run the vision code on them one at a time (`geomVisionLock`); what they assert
  is unchanged. The concurrency tests keep their goroutines, as the race step needs them. The
  always-on sizes are in section 19.
  **The long run (`QRVT_LONG_TESTS=1`, `go run ./tools/dev test -long`) runs in no CI job**; the
  nightly simulator-matrix job is still a placeholder, and a nightly long-test job is deferred to M6
  (section 19). That deferral covers the placeholder too: the nightly "full simulator matrix (M3)"
  below is not built in M3 but decided at M6 with the long-test job (designer's ruling,
  2026-11-19). Until then the long-only assertions are checked locally, by the per-pull-request
  rule and at each milestone exit (below), and by no CI job. Every test that reads
  `QRVT_LONG_TESTS` (directly, or through `geomLong`, `geomLongSize`, `longTests` or LDPC's `long`),
  with what only its long version asserts:
  - root package: `TestRoundTripMatrix`, every row with each preset's own camera and
    `matrixSessions` sessions (PR #47); `TestRoundTripMatrixX264`, its long rows with
    `x264Sessions` sessions; `TestSigningEndToEnd`, the unsigned recording (PR #49).
  - `internal/pipeline`: `TestHostileRecordingResources`, 32 frames at 3840 x 2160 with GOMAXPROCS
    16 and 32, **a SECURITY.md section 9.5 resource bound** (always: 16 frames at 848 x 480);
    `TestLocatedRecordingResources` at 4K and `TestTamperedStreamEndToEnd`'s full tamper matrix
    and `TestInterleavedSessions`' second order (PR #49); `TestEvictionLoopCost` (long only);
    `TestFrameLoss` at 100 frames and `TestLoopback`'s nine pairs (PR #47).
  - `internal/vision`, long only: `TestGeometryPresetsLong`, `TestGeometryHarshLong`,
    `TestGeometryHarshRegression`, `TestDetectModuleSweep`, `TestGlareStudy`, `TestDecoyMatrix`,
    `TestDecoyMatrixReview`, `TestPipelineMixedCost`; long sizes: `TestHonestConfidence`,
    `TestPipelineHonesty`, `TestPhaseWeights`, `TestPipelinePhase`, `TestPipelineGlare`,
    `TestGlareUnknown`, `TestStripeCancellation`, and the eight the vision test trim moved
    (section 19).
  - `internal/fec/ldpc`, long only: `TestErrorRateCurves`, `TestPairedM1B`,
    `TestDecoderCandidates`, `TestSoftBitUnits`, `TestMinSumVariants`; long sizes: `TestErrorRate`
    (20,000 frames per rate) and `TestShortenedErrorRate` (20,000 frames per check).
  - `internal/fec/conv`: `TestCorrectsThreeErrors`, exhaustive over three errors.
  - `spikes/pattern`: `TestAnalyseSimulated` at 96 captures.
  Race-step timings are quoted only from runs that retested the packages, never from output that
  shows them as `(cached)`.
- **Nightly** (`nightly.yml`, also startable by hand): long fuzzing (5 minutes per target by default),
  keeping the grown corpus in the Actions cache between nights and uploading any failing input; the full
  simulator matrix (M3), a 1 GB loopback with memory assertions (M6) and benchmarks with regression
  alerts (M6) run as soon as their `tools/dev` tasks exist and skip with a notice until then. The
  full simulator matrix's step stays a placeholder until M6, under the deferral of the nightly
  long-test job (section 19).
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
- **Change logs**: every change to DESIGN, FORMAT, SECURITY or IMPLEMENTATION_PROMPT is recorded in
  a file of its own, `docs/changes/<document>/YYYY-MM-DD-<topic>.md` (`go run ./tools/dev changelog
  add <document> <topic> "<text>"`), so two branches never edit the same lines and no change-log
  entry touches a document that tests read. The date is UTC (the tool uses the UTC date). Read in
  name order the files are the log (`changelog show`), dated to the day and ordered by topic within
  a day; each directory starts with the document's earlier table, moved there unchanged. A topic
  best starts with the stream, so names do not collide, and several entries from one pull request
  on one day take a number after the stream to keep their order (`m2-radiometry-1-gain`,
  `m2-radiometry-2-noise`).
- **Local checks before every push** (the owner's standing instruction, from 2026-11-16 on): one
  stream at a time; the independent review runs on the pushed branch before any pull request is
  opened; and a pull request gets one CI run at the end, a second only when a CI runner finds what
  the local checks cannot. Before every push, besides `test`, `cover`, `lint`, `test -race` and
  `build -all`, the developer runs `go run ./tools/dev platforms`. That routine run leaves out the
  linux/arm64 step (about 14 minutes on the machine below). Once per pull request, immediately
  before the push that opens it, `go run ./tools/dev platforms -full` runs every step and fails if
  any step could not run, and it takes no `-skip` (`-strict` fails on steps that could not run
  alone; `-skip` leaves named steps out of a routine run and refuses unknown names). A step that
  could not run is reported as NOT RUN with its reason, never as a pass. **The long tests**, which
  no CI job runs (see the coverage-step paragraph above): at the same point, once per pull request,
  the developer runs them for the packages the pull request changes, and also for the root package
  and `internal/pipeline` whenever the non-test code of any internal package changes; a docs-only
  pull request runs none; a pull request changing only tests runs the long tests of those packages
  only. `go run ./tools/dev test -long -pkg ./internal/vision,./internal/fec/ldpc` runs the listed
  packages in one `go test` run, without the test task's isolation split, so the root package,
  `internal/pipeline` and `internal/video` each run in a `-pkg` invocation of their own (for
  example `-pkg .`, then `-pkg ./internal/pipeline`). The result goes in the pull request's text:
  wall time, pass or fail per package, and what was skipped. The full long run, every package
  (`go run ./tools/dev test -long`), runs once at each milestone exit, locally, and its result goes in the exit check.
  The task gives each package 120 minutes (`-timeout=120m`; given with `-race` too, it comes after
  and replaces the race task's 18 minutes), so that a hang fails: at go test's
  default of 10 minutes the root package (in `TestRoundTripMatrix`), `internal/fec/ldpc`
  (`TestDecoderCandidates`) and `internal/vision` (`TestDetectModuleSweep`) panicked with the rest
  of their tests unrun, and the slowest package takes 54 minutes. With no limit, at the vision test
  trim's head on the machine below, the long run passed every package in
  90 min 51 s: `internal/vision` 3,249 s and `internal/fec/ldpc` 3,141 s side by side, then the
  root package 1,617 s, `internal/pipeline` 163 s and `internal/video` 155 s each alone; the only
  skips were the two helper tests that run only as a child process (`TestWorkerCountChild`,
  `TestStoreWriterProcess`). It is longer than `platforms -full` (about 48 minutes) on its own, so
  folding it into that task would take the pair past two hours; that run counts as the vision test
  trim's long run.
  - *compile*: `go vet` and `go test -c` of every package for all six targets, `CGO_ENABLED=0`, with
    no tags, the release build tags, and the release tags with the HEVC fast path's
    `unsandboxedhevc` (the set of CI's "HEVC fast-path tests" step). This catches build-constraint files, signature changes and
    code that compiles only on some systems.
  - *lint*: golangci-lint with `GOOS` linux, darwin and windows, each with and without the tags.
  - *race*: race-enabled test binaries built with zig 0.17.1 as the C cross-compiler (`CC="zig cc
    -target …"`, `CGO_ENABLED=1`; found through `ZIG` or `PATH`, installed by `scripts/setup.sh
    --cross`). They build for linux/arm64, windows/amd64 (with the import library of
    `WaitOnAddress` and a low image base, without which the race runtime cannot map its shadow
    memory), darwin/amd64 and darwin/arm64. windows/arm64 has no race detector, and linux/amd64 is the
    test task's own `-race`. The darwin binaries are only built, and so are the linux/arm64 ones:
    they need an aarch64 C library to run. The release stays pure Go with `CGO_ENABLED=0`.
  - *windows*: the windows/amd64 test binaries run through WSL interop, plain and race, each from
    its package directory in a copy of the module's files (tracked, and untracked but not ignored;
    never `recordings/`) under the Windows user's `%TEMP%`. They run through `cmd.exe` with full
    Windows paths, so that tests which start themselves again (engine workers, Job Objects) find
    their binary. `internal/video` also runs a third time, built with the HEVC fast path's tag set,
    as CI's fast-path step does: run 1 of the pull request that added this task failed in that step
    on windows-2025, which the local run had not built (a timing check in `TestReservationLoadLimit`
    read 9 µs under its 2 s bound, between wall-clock readings and the monotonic timer; the engine's
    meter now records monotonic offsets from a fixed base and the test asserts the bound exactly). The Windows-only tests run there (Job Object, sparse files, path rules);
    `internal/video`'s race tests do not, because its race-enabled engine workers cannot allocate
    ThreadSanitizer memory under the zig-linked race runtime; the task reports them as CI-ONLY,
    which `-full` accepts. Outside WSL the step reports NOT RUN.
  - *arm64*: the linux/arm64 test binaries run natively on an arm64 Linux host, else under user-mode
    emulation through an enabled binfmt entry for aarch64 (`qemu-user-static`, installed by
    `scripts/setup.sh --cross` where the script has usable root rights), with `QRVT_EMULATED=1` and
    a 90-minute limit per test binary; `internal/video` runs with the fast-path tag set too. qemu without binfmt is reported as NOT RUN, because tests
    that start their own binary again need binfmt. Under emulation `internal/video`'s
    `TestHostileFiles` logs instead of asserting its 60 s bound per hostile file (`nostsd.mp4` took
    65 s); the step prints that list and every bound it logged. Every other test asserts as it
    does natively.
  - An interrupt (Ctrl-C, SIGTERM) stops the running command, kills a Windows test binary and its
    engine workers by the binary's full path in this run's directory (never by image name, which
    would match another run's), reports the interruption once as INTERRUPTED, which fails the run
    in every mode, and removes the temporary directories; directories that an earlier run left
    behind more than six hours ago are removed at the start.
  - The task prints, per package, the time, the tests passed and the tests skipped. On the
    developer machine of spike S1 (16 threads, WSL2) a `-full` run took 48 min 09 s with the build
    cache warm: compile (three tag sets) 35 s, lint 29 s, race builds 8 s, Windows 13 min 13 s
    (plain, fast path and race), and linux/arm64 under emulation 33 min 43 s (with its fast-path run
    of `internal/video`, 6 min 28 s); the routine run is the same without linux/arm64, about 14
    minutes. A first run with a cold build cache took
    49 min 40 s (compile 51 s, lint 45 s, race builds 1 min 23 s).
  - **Not covered locally**, accepted as CI-only, so a CI run can still find something: running
    anything on macOS (amd64 or arm64), running the windows/arm64 binaries, native arm64 timing on
    the arm runners, race runs on linux/arm64 and darwin, and `internal/video`'s race tests on
    Windows. The task prints this list.
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
| M3 | Pipeline, public API, merging of recordings, hostile-input suite; check that the digital loopback still passes with margin when the glare rule discards 1.46-2.80% of black-and-white cells of a perfect frame, mean about 2.1% (section 10, step 4). `tools/dev testvideos` and the committed "as filmed" clips moved to M5, and the fuzz corpora to M7 (designer's ruling, 2026-11-19) |
| M4 | `calibrate`, `inspect`, profiles |
| G1 | The owner films the calibration video and sample transfers, including one tripod take |
| M5 | Real-device tuning of every mode, of merging and of stripe cancellation; 10-bit recordings; inferring the frame index of a capture whose header copies all fail, from the last index and the phase markers with the quad carried by tracking (deferred from M2, section 7 rule 1); mirrored recordings (FORMAT.md section 4.1); `tools/dev testvideos` and the committed synthetic "as filmed" clips (section 15, test videos), moved from M3 because M5 is where they are used, their size budget (the 2 MB class) ruled then |
| M6 | 1 GB scale, resuming with `--state-dir`; experimental `--hdr` |
| G2 | The owner films one large transfer |
| M7 | Final security pass; committed corpora for the 9 fuzz targets that have none (SECURITY.md section 9.1), moved from M3 to be built alongside `FuzzDecode`'s reach (section 19) |
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

### Open issues

- **The laptop recordings' corners (S2).** When the code fills the frame to its corners, as on the
  S2 laptop recordings (a 3200 x 2000 screen filmed by two phones), the corner finders lie beyond
  every anchor, where the phones' distortion departs from the k1 + k2 lens model (section 10, step
  3) by up to a finder module; the leave-one-out check then rejects a real finder, and the frame is
  not located. A third lens term, or a tolerance wider than half a module, were ruled out
  (2026-11-17). Located captures before (M2 at de1d1c4) and after the S2 geometry fix, and the
  left finders' offsets from the anchors' prediction on 30 frames:

  | Recording | Located before | Located after | Left finders' offsets |
  | --- | --- | --- | --- |
  | Phone A, recording 3 (4K60) | 459 of 4,674 | 352 | 3.6 and 2.2 cells (1.0 and 0.6 module) |
  | Phone B, recording 4 (1080p30) | 0 of 2,326 | 883 | 2.3-2.6 and 1.3-1.4 cells (0.65 and 0.35 module) |
  | Phone B, recording 5 (4K30) | 4 of 2,257 | 34 | not sampled |
  | Phone B, recording 6 (4K60, 8-bit) | 3 of 4,797 | 814 | 1.1 and 0.5 cells (0.6 and 0.27 module) |

  Over all six recordings (the full S2 analysis, captures located before and after; recording 3
  regressed and is on record here, not yet diagnosed):

  | Recording | Before | After | Captures |
  | --- | --- | --- | --- |
  | Phone A, recording 1 (rescaled playback) | 889 | 3,255 | 3,372 |
  | Phone A, recording 2 | 2,836 | 2,947 | 3,422 |
  | Phone A, recording 3 (laptop) | 459 | 352 | 4,674 |
  | Phone B, recording 4 (laptop) | 0 | 883 | 2,326 |
  | Phone B, recording 5 (laptop) | 4 | 34 | 2,257 |
  | Phone B, recording 6 (laptop) | 3 | 814 | 4,797 |
  | All | 4,191 | 8,285 | 20,848 |

  With the check disabled locally, recording 3 was located in 4,290 captures and its left 10% of
  columns fell from 40-49% confident errors to 2-8%. Candidate fix, to evaluate once the
  end-to-end path works: locate from the anchor lattice and a decoded header; treat a corner
  finder that fails the confirmation as absent; mark the cells outside the anchors' hull at that
  corner unknown instead of rejecting the frame. `TestGeometryMoustacheCorner` is skipped until
  then: the simulator's lens is the detector's own model, which extrapolates correctly.
  **Recording 3's regression is assigned to M5** (designer's ruling, 2026-11-19), with the real-device
  tuning that diagnoses it.
- **The acceptance measure is not met on rescaled playback (S2, Phone A, recording 1).** After the
  S2 geometry fix, the confident-error rate (bits with |s| of 39 and more) at the code's edges
  improved but does not match the interior. By segment, the left 10% of columns went from
  0.5-8% to 0-1.1e-2 in the Gray2 and Gray4 segments, and in RGB8 to up to 4.4e-2 at 4-pixel
  cells and at most 4.1e-3 at 8 and 12; the right 10%
  went from 1.2-22% to 0-3.3e-2 in Gray2 and Gray4 (for example rate 30, 12-pixel cells, Gray4:
  7.8e-3 against 1.8e-5 inside; rate 60, 12-pixel cells, Gray4: 3.3e-2 against 2.9e-5) and up to
  7.6e-2 in RGB8. The interior's 60% of columns holds 4e-7 to 4.7e-3. Recording 2's edges match
  its interior. This does not block the fix; M3's decode attempt on the real recording will
  decide whether it matters.
- **Wrong quads below the reliable sizes.** In the long `TestDetectModuleSweep` (200 poses per
  preset and size), wrong quads occur only below the reliable sizes of section 10, step 1, on
  M2 (de1d1c4) as on the S2 geometry fix, and the fix has more of them in some cells:

  | Preset, camera pixels per cell | Wrong quads, M2 | Wrong quads, S2 fix |
  | --- | --- | --- |
  | handheld, 2.8 | 2 | 2 |
  | handheld, 3.2 | 4 | 3 |
  | handheld, 3.6 | 1 | 1 |
  | budget-android, 2.8 | 0 | 3 |
  | budget-android, 3.2 | 0 | 3 |
  | iphone-hdr, 3.2 | 2 | 6 |
  | iphone-hdr, 3.6 | 1 | 2 |
  | harsh, 4.8 | 1 | 1 |

  None of their headers was wrong. The located counts there fell slightly too (budget-android at
  3.2: 192 to 186; iphone-hdr at 3.2: 81 to 71). A wrong quad below the reliable sizes relies on the header's
  CRC and, downstream, on each codeword's CRC to keep wrong cells out of the payload. Nothing is
  tuned to move these numbers. **Assigned to M5** (designer's ruling, 2026-11-19).
- **Small grids.** The confirmation needs eight located anchors (section 10, step 1), and the
  anchor lattice (every 32 columns and 30 rows) gives small grids fewer, so they are never located,
  even on a perfect capture where the header reads and every anchor is found:

  | Grid | Presets | Anchors | Located |
  | --- | --- | --- | --- |
  | 53 x 30 | 480p at 16-pixel cells | 2 | no |
  | 80 x 45 | 720p at 16 | 2 | no |
  | 80 x 60 | a 960 x 720 test plan at 12 | 4 | no |
  | 106 x 60 | 480p at 8, 720p at 12 | 6 | no |
  | 120 x 90 | | 12 | yes |

  A finder module is H/135 pixels whatever the cell, so the finders and the header are large
  enough; the fix is a denser anchor lattice for small grids, a FORMAT change with its own pull
  request. `TestDetectSmallGrids` characterises today's behaviour. Encoding does not refuse these
  grids.
- **Detection time, for the performance pass after the end-to-end path works.** The S2 geometry
  fix widened the header's lens search (up to 25 candidates with k2, against 13) and refits the
  global model as anchors accumulate. On typical captures it is no slower: Detect's median on 12
  handheld poses went from 33-39 to 35-37 ms at 1080p and from 75-78 to 70 ms at 4K (with header
  and mesh, 42-51 to 43-45 and 95-98 to 85 ms). The cost falls on frames whose header reads only
  through a strong lens or never: the cost reference (the real code through a lens at the edge of
  the range, SECURITY.md section 9.1) takes 33 ms at 1080p and 56 ms at 4K, against 18 and 44 for
  the rendered frame, and the 4K gate header 196 ms, 3.5 times the reference against its bound of
  6 (5.3 times against the rendered frame, before the reference was redefined). The hostile noise
  frame's 301 ms is the figure to beat. **Assigned to M6** (designer's ruling, 2026-11-19), the
  performance and scale work.
- **Infeasible, not a defect (designer's ruling, 2026-11-18): `budget-android` 720p12 Gray4
  `normal`.** The row decoded in 1 of 5 repeatable sessions with the stand-in and 0 of 3 with x264.
  It was traced in one bounded pass with the simulator's truth, on the row's own setting (320 x 180
  grid, 1080p30 camera, 12 fps display, 4.62 measured camera pixels per cell).

  **How the figures were produced.** A throwaway diagnostic test in `internal/vision`, not in the
  tree, built on the radiometry bench:
  - The bench session is `newRadSession` with 12 code frames carrying inner-code codewords (seed
    0xb0d6).
  - It is filmed by the preset at 12 fps for 24 captures (simulator seed 0x77; 12 or 36 for the
    ablations), with the grid varied to vary the camera pixels per cell.
  - Every capture goes through Detect, the mesh fitter, the footprint sampler, `SamplePalette`, the
    phase field, the equaliser with stripe cancellation and `SoftBits`.
  - For each credited phase, the frame's codewords are decoded as the stripe test does
    (`decodeFrame`: codewords with at least 90% of bits stated).
  - The inner code is switched for `fast` and `robust` by a temporary change to the bench's header.
  - **Not a vision defect.** The raw bit error rate is 0.107 at true positions and 0.109 through
    the pipeline. The mesh is within 0.037 cell RMS. Per-tile oracle levels taken from the truth
    still give 0.102. Cells wholly one frame and partly mixed ones are alike (0.108 and 0.112).
  - **Systematic.** 81-90% of the cells wrong once are wrong in every capture of their frame.
  - **Cause: the optical blur.** Without it the pipeline's bit error rate falls to 0.0024. Without
    noise, shake, rolling-shutter readout or the recorder quantiser it does not move. A cell's four
    neighbours leak into it at 0.51 of its own weight.
  - **The blur chain** matches section 4's model in units (sigma in camera pixels) and order. It
    differs in applying the blur in linear light before the tone curve, which is physically right,
    and in sharpening. A scalar model of both gives a symbol error rate of 0.21 at 4.8 pixels per
    cell, against 0.10 when blurring code values as section 4 did.
  - **Decodability.** Per capture through the pipeline, 9.5% of `normal` codewords decode at 4.62
    measured pixels per cell (4.8 nominal), against 99% at 5.20 (5.4 nominal). A hard-decision
    oracle bound gives 0.73 bits per sent bit against the 0.78 `normal` needs. Soft decoding does
    better than that bound, so the codeword figures are the evidence.

  Section 11's condition 2 is re-derived accordingly, and the row is removed from the matrix
  (section 15). That is this row's M3 exit. The other `budget-android` rows decode in all sessions:
  5 of 5 with the stand-in and 3 of 3 with x264 for black and white `normal` and `robust` and for
  four greys `robust`.
- **The handheld planning rates are simulated (section 11, 2026-11-18).** They await the G1
  recordings. S2's real net rate, about 3.3% of the recording's bitrate at a setting decodable with
  margin, remains the more pessimistic evidence for the 4K rows. At the assumed bitrates it gives
  about 95 KB/s for 4K30 (23 Mbit/s) and 220 KB/s for 4K60 (53), against the handheld table's 112
  and 280.
- **Candidate for M5: neighbour-aware equalisation.** On the row above, at true positions on clean
  cells, a per-capture fit of each cell's level against its edge and diagonal neighbours' mean levels,
  with one round of decision feedback, lowers the four-grey symbol error rate from 0.226 to about
  0.11 (0.108 with centre-point sampling). An oracle that knows the true neighbours and keeps one
  template per own level and neighbour configuration reaches 0.094. Not built: it would be a new step
  in section 10, and it widens the blur any setting tolerates without changing the conditions above.
- **Bench defect: the radiometry bench's true-position sampler.** With the simulator's blur off
  (`budget-android`, 320 x 180 grid, 1080p30), observations taken at true positions through
  `radCapture.region` give a bit error rate of 0.098, where the pipeline gives 0.0024. Oracle levels
  on the same bench samples give 0.0001, and mapping every cell corner exactly (`nodeStep` 1) changes
  nothing. So the fault is in what the bench feeds the equaliser (its palette or anchor sampling),
  not in its cell sampling. Not a few-line fix, so not fixed. Results that use the bench with blur
  off should not be trusted until it is. **Assigned to M5** (designer's ruling, 2026-11-19).
- **Coverage margins (M3 part 2).** PR #48's first run failed the coverage gate on windows-11-arm
  alone: `internal/pipeline` at 95.0% (95.2 to 95.4% on the other runners; the error paths of the
  repair, the shard store and the hash check were never covered, and two cancellation returns
  only sometimes). Deterministic tests of those paths bring it to 96.5% (measured over three runs, with the
  defensive guard in `attemptDue` kept uncovered). `internal/video` stood at 95.8% (windows-2025) and 95.6% (windows-11-arm)
  in that run: a margin item for the next pull request that changes `internal/video`, not fixed
  in this one. **Assigned to M5** (designer's ruling, 2026-11-19).
- **Round-trip matrix with x264 (M3 part 2).** `TestRoundTripMatrixX264` with `QRVT_LONG_TESTS=1`
  runs the 13 rows that have a recorder bitrate (not `ideal`, whose recorder is lossless) with
  x264 at the preset's bitrate and camera, three sessions each, 855 s on the development machine:
  12 rows decode in 3 of 3 sessions; `budget-android` Gray4 `normal` in none (previous entry;
  before the row's removal on 2026-11-18).
  Filming dominates the cost: about 18 to 21 s a session at 4K, 53 s for `harsh` (3 passes,
  4K30), 5 to 11 s at `budget-android`'s 1080p. The short version runs one row, `budget-android`
  Gray2 `normal` with its own 1080p camera, about 8 s pinned to 4 CPUs (designer's ruling on the
  CI budget; `tripod` Gray4 `normal` at the smaller camera, 13 s more, runs in the long version
  with the rest). Also measured, not asserted, and not diagnosed: `iphone-hdr` Gray4 `normal` with
  x264 at the short matrix's camera, 3008 x 1692, falls short by 55 shards, where the stand-in at
  that camera and x264 at the preset's 4K camera both decode. The bitrate there was scaled with
  the pixel count by `channel.Config.Resize`: 74 Mbit/s x (3008 x 1692) / (3840 x 2160), about
  45.4 Mbit/s, the same bits per camera pixel. That camera gives 5.0 camera pixels per cell
  (fill 0.80 x 3008 / 480), below the 6 that section 11 now requires for four greys `normal` under
  the handheld blur: the setting is infeasible by that condition (2026-11-18), and the shortfall
  with x264 is expected. Through the vision pipeline at that camera, 25% of `normal` codewords
  decode per capture, against 100% at the preset's 4K camera. The stand-in still decodes there,
  with two captures per code frame. The CI row, filmed at that camera with the stand-in, is
  asserted below the condition by design and is `TestMatrixFeasible`'s one exemption (section 15).
  **If it ever fails in CI, it is replaced by `iphone-hdr` four greys `robust`, and is not rerun.**
  Also open: no 10-bit x264 recording runs end to end outside the long version (`iphone-hdr` is
  its only 10-bit row, and the engine's 10-bit round trip of a recording,
  `TestEncodeBitrateRoundTrip`, decodes a synthetic clip). **Assigned to M6, with `--hdr`**
  (designer's ruling, 2026-11-19).
- **Round-trip matrix (M3): one row fails.** `TestRoundTripMatrix` with `QRVT_LONG_TESTS=1` runs
  every row of section 15 through the public API with five repeatable sessions each: the files
  (6.5 KB) are encoded to an MP4 by the embedded engine, decoded, filmed for two passes (three on
  `harsh`) by the preset's camera with the pure-Go recorder stand-in, not x264 at the preset's
  bitrate, and decoded. 21 of the 22 rows decode in all five sessions. `budget-android`, 720p at
  12 fps, Gray4 `normal` decodes in 1 of 5 (short by 1 to 6 shards of one block), and in 1 of 8
  further random sessions with 3 passes. The cause is the channel, not the summing: each capture
  shows a raw bit error rate of about 0.10 (4.8 camera pixels per cell, blur 1.5, sharpening 0.8,
  quantiser step about 11), and the errors repeat between captures of a frame, so summing them
  gains little. The row meets section 11's feasibility conditions (camera at 2.5 times the
  display rate, 4.8 pixels per cell against 4.5, q = 22 against 7.7), so it was a defect to
  trace in vision or the simulator. It was traced on 2026-11-18, and the condition was found
  wrong: the row is infeasible and was removed (the entry above). CI runs one row per preset (Gray2 `normal` for
  `budget-android`), one session each. The matrix's payload is small (F 4 to 12), so it does
  not exercise many blocks. **The matrix does not yet use x264 at the preset's bitrate** as
  section 15 asks (`video.EncodeConfig` has a quantiser, no bitrate); the next M3 piece adds it
  (designer's ruling, 2026-11-17; section 15 unchanged). Burst loss is tested on a perfect
  recording only (14.7% of the frames lost in one run decodes), with one block, where a burst
  and scattered loss cost the same.
- **Vision under strong simulated noise (M3).** Before the inner code fails, location and the
  crediting of cells do. On a perfect 480p Gray4 capture with Gaussian noise added per pixel, the
  credited share is 0.92 at sigma 10 and 0.22 at 12 with no bit error, and nothing at 15; the
  phase field returns zero weights once anchor quadrants hold a sample at 0 or 255. The `ideal`
  preset with sensor noise 12 (camera 1080p) credits nothing; `tripod` at 1080p holds to noise
  12 and fails at 16. With noise on the data cells only, detection fails first. So
  `TestSoftBitsAddUp` gives the decoder soft bits from a BPSK channel directly instead of a
  filmed recording. A decode-level version was tried with the whole capture's contrast reduced
  to 0.3 and noise on the data cells only: at sigma 14 each recording decodes 3 or 4 of 4
  frames, and from sigma 16 location fails, so no setting isolates the summing; not added.
- **Real recordings through the decode path (M3, `-tags realdevice`).** The S2 pattern carries
  no codewords, so the public decode finds no descriptor (recording 1: no blocks known, after
  255 s) and the check decodes each frame's summed soft bits against the all-zero codeword with
  the inner code and its CRC (the coset of the all-zero shard; `internal/pipeline`
  `realdevice_test.go`), with `normal` and, through `robust`'s interleaver, `robust`. Over the
  six recordings no codeword decoded to anything but zeros. At the settings S2 calls
  conservative: `normal` at a 12 fps display with Gray2 or Gray4 at 6 or more camera pixels per
  cell decoded every codeword tried on recordings 1 and 2; at a 30 fps display filmed at 60 fps
  (recording 6, 7.8 pixels per cell) Gray2 6,786 of 6,842 and Gray4 11,252 of 11,252; `robust` at
  a 30 fps display filmed at 30 fps decoded 93-100% on recording 1 (Gray4 at 4-pixel cells 13,860
  of 14,630) and on recording 2 Gray2 at 4-pixel cells 8,827 of 8,932 but Gray4 only 5,381 of
  18,018 (the 29.98 fps camera beating against the display). RGB8 at 4-pixel cells decoded
  nothing with `normal` anywhere. Laptop recordings 3 to 5 are limited by location (the corners
  issue above). No margin recordings exist yet.
- **CI-size suites near the runner's limits (M3, performance, not optimised now).** PR #47's
  first run failed on macos-26-intel only: in the coverage step `internal/vision` reached `go
  test`'s 10-minute default (600.6 s), running beside the two packages M3 adds (root 449.9 s,
  `internal/pipeline` 344.5 s; `internal/payload` 221.9 s, `spikes/pattern` 79.8 s,
  `internal/video` 183.3 s after them). The always-on end-to-end tests were trimmed, with every
  removed row, session and case still asserted with `QRVT_LONG_TESTS=1`: the matrix's short
  version films half a pass, then a pass, and the whole recording only if those fall short (the
  simulator's captures are prefix-stable, `TestFilmPrefix`, and decoding stops once complete, so
  the outcome is the whole recording's); `TestFrameLoss` uses a loop of at least 52 frames (100
  in the long version) with both losses rounded up; `TestLoopback` runs each modulation and
  protection once (all nine pairs in the long version); the tests that feed the decoder directly
  share one encoded transfer; the short matrix films each row with the smallest 16:9 camera, at
  least 1080p, that gives the code 5 camera pixels per cell (section 11's planning point), at the
  preset's rate, timing, blur and shake (ideal 1920 x 1080, tripod 2688 x 1512, handheld and
  iphone-hdr 3008 x 1692, budget-android its own 1920 x 1080, harsh 2304 x 1296); the long
  version keeps each preset's own camera. Measured on the development machine with the coverage
  step pinned to 4 CPUs (`taskset -c 0-3`, GOMAXPROCS 4, uncached), at PR #47's first head and
  after these trims, before isolation: root 80.8 and 47.2 s, `internal/pipeline` 58.5 and 38.8
  s, `internal/vision` 142.7 and 126.4 s, the whole step 313 s after; alone, root about 20 s and
  pipeline about 16 s. The runner took 5.6 and 5.9 times the pinned figures for root and
  pipeline. Two changes follow (designer's ruling on PR #47). **Isolation:** `tools/dev` runs
  the root package and `internal/pipeline` each on its own after the other packages, before
  `internal/video` (`isolatedRuns`; the coverage gate merges every run's profile; under the race detector they
  later moved to the isolated part, below). That adds roughly 3 to 4 minutes on the slowest
  runner, (20 + 16) s x 5.7, against the non-race steps' 30 minutes, of which they took about 22
  before this pull request; it returns `internal/vision` to its conditions before it, about 530
  s on that runner; and the 5.7-times projection of the whole contended step no longer applies.
  **Timeout:** the coverage step sets `-timeout=15m` per package (`coverArgs`), because go
  test's 10-minute default was never a chosen limit; the job's 70 minutes and the 30 for the
  non-race steps are the real guard. `internal/vision`'s cost at CI size is a performance item,
  not optimised now. PR #47's second run (6f18fc7) passed the coverage step in 19 min 22 s,
  `internal/vision` taking 662 s under the 15-minute limit, but on that host the race step for
  the other packages reached its 20 minutes: the main group took about 12 minutes
  (`internal/vision` 709 s, `internal/payload` 507 s, `spikes/pattern` 469 s), the root package
  then 128 s, and `internal/pipeline` was cut off; the job stood at about 58 of its 70 minutes.
  So (designer's ruling): under the race detector the root package and `internal/pipeline` run
  a subset, as `internal/vision` does (`raceSize`, `raceSkipStatistics`): the API's round trip,
  and every pipeline test that crosses a goroutine boundary or guards a limit (two recordings,
  cancellation, the reorder bound, disk space, hostile descriptors, frame loss at 20 frames,
  where 3 lost are 15% and 4 are 20%, so the levels stay honest); the matrix, the typed errors,
  the prefix property, the mid-loop start and the summed soft bits run in the non-race step. PR B
  of M3 part 2 adds to that step the hostile and located recordings' resources, the tampered stream
  and the interleaved sessions end to end, `FuzzDecode` (also in the fuzz step), signing end to end
  (root package) and the two eviction tests. Five of those (the hostile recordings, the tampered
  stream, the interleaved sessions, `FuzzDecode` and signing) and the located recordings run the
  whole `Decode`; they are skipped for time, not because they cross no goroutine. The `Decode`
  goroutine paths they use stay under the race detector through `TestEncodeDecode` (root), the
  pipeline's loopback and frame-loss tests and `TestWorkersClampedWarning`, and the grid stage's
  gate through `TestStageGateGridChange` (32 goroutines). And
  both run in the isolated race part beside `internal/video` (`-part isolated`), so the other
  race step holds what it held before this pull request; the steps stay two, with their limits.
  The CI-size suites and that runner's variance from host to host are an item for later, not
  optimised now. PR #47's third run took 818 s for the coverage step on macos-26-intel, against 334 s
  pinned on the development machine: about 2.5 times, so the 5.7 times above, taken from the
  contended step, overstates an uncontended one. The margin is kept anyway, because that
  runner's speed varies from host to host.
- **PR #49's CI runs 2 and 3 (M3 part 2, PR B).** Run 2 failed two steps. (1) CI's
  go-test-coverage step gates a package as a file's path up to its last slash, so each file at the
  module's root is gated on its own; `qrvt.go` stood at 93.9% (62/66) where `tools/dev cover`,
  grouping by directory, passed the root package at 95.4%. Two tests now reach two of its four
  uncovered statements (97.0%; `convert.go` 96.9%, 31 of 32, so one more uncovered statement
  there would take it to about 93.8%, below the per-file gate); the other two, a failure of `crypto/rand` in
  `GenerateKey` and of `json.MarshalIndent` on the report, no test reaches without a production
  change. `tools/dev cover` now groups files as the action does (`TestParseProfileGroups`), so the
  local gate fails where CI's would. (2) The coverage step on macos-26-intel reached its 20-minute
  limit; run 1 on the same code took 18 min 13 s, and package times rose from run 1 to run 2
  (`internal/vision` 605 to 714 s, the root 149 to 200 s, `internal/pipeline` 95 to 119 s). This
  PR's heavier always-on cases moved to the long run, where they still execute, keeping one small
  case per property always (designer's ruling): the located recordings' resources at 4K (always,
  one pass of the transfer's captures at 848 x 480, decoded correctly within 1 GiB and the grid
  stage's budget, 0.7 s pinned), signing end to end with an unsigned
  recording (`TestTypedErrors` refuses one always), the tampered stream beyond the stream as
  signed, a flipped body bit without a trusted key and a flipped signature bit with one, the
  second order of the interleaved sessions; `TestEvictionCycle` runs 1,000 captures, not 3,000.
  Pinned on 4 CPUs, back to back on the development machine: the coverage step took 365 s at #48
  and 377 s here (before the last trim); the 346 s measured for #48 earlier came from a faster
  run of the same machine, and `internal/vision`, unchanged, moved by 5 s between the two runs. At
  bce3915, back to back: 464 s at #48 and 477 s here (+2.8%; the reviewer's, 455.5 and 470.6 s,
  +3%), within the designer's 5% of #48 back to back, with no always-on security case dropped.
  **The main margin risk for the coverage step was `internal/vision`'s CI-size cost**, 600 to 714 s
  on macos-26-intel. The vision test trim (2026-11-18) cut the package, pinned to 4 CPUs and
  uncached, back to back on the development machine, from 127.7 and 126.8 s to 89.6 and 84.8 s
  (-31%). On macos-26-intel PR #51's CI run (44453075933) measured `internal/vision` at 264.3 s
  in the coverage step, down from 600 to 714 s, about -60% (the projection had been 400 to 450 s). No bound, tolerance or ratio changed. Three means: the hostile-cost
  tests (about 20 s) skip in a coverage build and are asserted without coverage (section 17); the
  pose sweeps run their vision code one capture at a time in a coverage build (section 17); and
  these always-on cases shrank, their full sizes running with `QRVT_LONG_TESTS=1`:
  `TestGeometryMoustache` 12 of its 40 poses (so at least 11 located), `TestGeometryMoustacheWindow`
  12 of 24, `TestDetectModuleLimit` 12 of 24 per case, `TestGeometryPresetsSmall` 12 of 24 per
  preset, `TestGeometryPresets4K` 2 of 4 per preset, `TestGeometry16x10` 6 of 12, `TestGlareOnFinder`
  2 poses (3 before, 6 in the long run) and `TestSampleGlareFlagged` 1 of 2. **Gap:** no CI job
  runs the long tests, so these eight full sizes, and the cases PR #49 moved (the located
  recordings' resources at 4K, the full signing and tamper matrices, the second interleave order),
  are checked only locally (section 17: per pull request for the packages it touches, and the full
  run at each milestone exit). **A nightly long-test job is deferred** (designer's ruling,
  2026-11-19): the full long run took about 91 minutes on the 16-thread development machine
  (`internal/vision` 3,249 s, `internal/fec/ldpc` 3,141 s, the root package 1,617 s) and is
  expected to take several hours on a 4-vCPU runner, so it needs splitting or long-size trims
  before it can be a CI job; the decision is due at M6, the scale work, which owns the nightly 1 GB
  loopback anyway. The deferral also covers the nightly workflow's "full simulator matrix (M3)"
  step, still a placeholder (section 17): it is decided at M6 with the long-test job, not built
  in M3 (designer's ruling, 2026-11-19). **Recorded, not fixed (reviewer's LOW, 2026-11-19):** most guards turn the long
  tests on only for `QRVT_LONG_TESTS=1` (`== "1"`), but three treat any non-empty value as on:
  `internal/vision`'s `geomLong`, `internal/fec/ldpc`'s `long` and `internal/fec/conv`'s
  `TestCorrectsThreeErrors`; so `QRVT_LONG_TESTS=0` runs the long sizes in those packages only.
  Making them all `== "1"` touches three places, more than the designer's two-helper limit for this
  pull request, so it waits for a later one: **assigned to M4's tooling work** (designer's ruling,
  2026-11-19). **Also for later (reviewer's LOW, 2026-11-19):**
  `tools/dev test -pkg` runs its packages in one `go test` run, without the isolation split that
  `./...` gets (`isolatedRuns`); until it goes through the same split, section 17 asks for the root
  package, `internal/pipeline` and `internal/video` in `-pkg` invocations of their own. **Assigned
  to M4's tooling work** (designer's ruling, 2026-11-19). The
  designer accepted -31% (2026-11-18), with the honesty statistics and the concurrency tests kept
  in full; the macos-26-intel figure above stays a projection until this pull request's CI run
  measures it. Half, the target, was not reached. What remains is
  mostly the parallel statistics: `TestHonestConfidence` (31 to 40 s within the run) and
  `TestPipelineHonesty` (17 to 22 s), whose CI size the designer kept so that every bucket is
  judged, then `TestPhaseWeights`, `TestGlareUnknown`, `TestPipelineGlare` and `TestPipelinePhase`.
  Filmed with one capture instead of two (an experiment, not committed), `TestPipelinePhase`'s
  tripod yield fell below its floor (0.9566 against 0.964), two `TestGlareUnknown` cases failed and
  one `TestPipelineGlare` case had no cell under its patch, so those sizes stay. Under the race
  detector the package is unchanged (125.7 and 122.6 s pinned). Noted, not tuned: in the honesty
  tests' profile the equaliser's `flagUnknown` (its `binomialTail`) takes 12.7% of the CPU.
- **Performance, noted and not tuned (M3).** Decoding reads each recording with one video
  decoder, always with the colour planes, and the owning goroutine runs every LDPC attempt itself;
  the encoder joins runs with one engine instance. Eviction rescanned every frame's soft bits each
  time the cap was exceeded, O(frames x C x sent bits) per eviction, until PR B of M3 part 2
  (below). Per-frame bookkeeping was
  outside the 256 MB cap: each frame seen (at most F; a capture can credit up to four frames,
  three from one header's phases and a fourth from a second header) keeps C
  `done` flags and C slice headers, about 25 C bytes: 3.8 KB at C = 151 (250 MB at F = 67,112),
  and about 200 KB at the largest grid's C, so a long recording claiming that grid could have held
  gigabytes. **Under the cap since M3 part 2:** a record counts 96 + 25 C bytes beside the soft
  bits; over the cap the least confident frame's soft bits go first, then the record of the frame
  seen least recently; the frames seen are counted in a bitset of F bits (at most 512 KiB, beside
  the cap). A frame seen again after its record went is decoded again, and its shards, already
  stored, are not counted twice (`TestBookkeepingUnderCap`, `TestDroppedRecordCountsOnce`).
  **Re-examine, for M7:** a hostile or looping recording that keeps the decode at the cap made
  every capture evict, rescan everything held (up to 256 MB) and decode codewords again: 489 ms a
  capture, beyond 60 s after about 120. Since PR B of M3 part 2 the eviction order lives in two
  heaps updated as soft bits are added (least confident first, then least recently seen, as
  before): 0.2 ms a capture at the full cap, and 200 captures there within 60 s and 1 GiB,
  asserted in the long run (`TestEvictionLoopCost`); the steps per capture do not grow with what is
  held, nor does a capture's time, which tells a rescan from the heaps (`TestEvictionWorkBounded`: 47 times at 1,600 frames against 40 for the rescan, about 1.1 for the heaps; `TestEvictionCycle`, always). Re-decoding a frame whose record
  went is bounded per capture by the codewords of the frames it credits. **Decode memory (PR B):**
  the vision workers had been one per CPU, about 50 MiB each at 4K, so a 4K noise recording grew
  the heap by 836 MiB at 16 CPUs and would pass 1 GiB at about 24; they are now capped by memory
  from the probed capture size, at half the decode's 640 MiB budget (SECURITY.md section 5: 6 at
  4K, 1 at 8K; 360 MiB for 4K noise at 16 and 32 CPUs). That model held for noise only (review of
  PR B): a capture that shows a code runs the grid stage, sampling, the tile equaliser with stripe
  cancellation (about 200 MiB a 4K capture) and the soft bits (about 64 MiB), whose memory grows
  with the grid. The reviewer measured the encoder's 4K output in modulation 3 (RGB8) before the 256 MB store
  filled: 510 MiB of heap at 1 worker, 1,224 MiB at 3, about 3.5 GiB at 12 (the cap then; 2 GiB
  even at GOGC=10, so live), and 1,863 MiB at 8K with 3; a header can claim about 6 times the
  cells at 4K. **The grid stage's memory model** (designer's ruling (a)): one capture's stage holds
  600 bytes a data cell and 16 MiB, from the heap's growth over one capture's stage on the
  encoder's output, modulations 1 to 3 measured (peak live about equal at 4K; modulation 3 allocates most); 4 (Gray8) is not produced by the encoder and is unmeasured: in modulation 1, 84 MiB for 117,112 cells (1920 x 1080, 4-pixel
  cells; 71 MiB in a second run), 283 MiB for 469,088 (3840 x 2160; 267 MiB in a second run), and
  modulations 2 and 3 at 4K 183 and 188 MiB; the frame's size does not enter (the 480 x 270 grid
  at 3840 x 2160 held 67 MiB, the 960 x 540 grid at 7680 x 4320 285 MiB). A gate admits the stage
  at that weight against what the workers' captures leave of the budget, at least half, in order of
  arrival; a stage whose weight exceeds the budget takes all of it and runs alone; nothing is
  refused (a refusal rule is a limit change, with its own design). Each capture's weight is its own
  grid's, so alternating grids cannot raise the concurrency (`TestStageGateGridChange`);
  `TestStageConcurrencyByGrid` asserts fewer at once as the grid grows, never fewer than one. The
  stage's buffers are the capture's, not kept by the worker. **Measured (long run,
  `TestLocatedRecordingResources`):** 24 captures of the 4K output at 3840 x 2160, 834 MiB of heap
  growth at GOMAXPROCS 16 and 883 at 32, one capture in the stage at a time, the soft-bit store
  nearly empty; the 1 GiB holds up to there. Beyond it, not met: the same decode with 256 MiB held
  to stand for a full store, as a hostile or a long poor recording reaches, 1,121 MiB of growth at
  GOMAXPROCS 16 and 1,106 at 32 besides the store (reviewer's measurement); and, logged: 8 captures at 7680 x 4320 of the 4K grid, one worker, 1,096
  MiB (1,114 in a first run: the reader's three capture buffers at 99.5 MB each and the GC's
  headroom over them; the memory model therefore counts three captures a worker, not two, as
  measured; the code's estimate, 6 x width x height, is unchanged); 6 captures at 3840 x 2160 of the
  largest grid a header can claim, 2,047 x 1,393 (2,567,107 cells, a model weight of 1,484 MiB, so
  alone), 1,799 MiB and 107 s, about 18 s a capture alone, so a recording of 4 or more such
  captures exceeds the 60 s bound as well. **Both bounds hold** for the encoder's 4K output with the
  soft-bit store nearly empty (12.3 and 12.6 s for 24 captures, 834 and 883 MiB) and for noise (848 x 480: 0.3 s, 126 MiB; 3840 x
  2160: 1.7 s, 360 MiB). No refusal and no time limit in this PR (designer's ruling): M7 decides
  between a refusal rule, a time limit and a tighter cap on the claimable grid, and for memory
  between a refusal rule and M6's allocation work, and whether the soft-bit store counts against
  the decode's budget. Outside the gate besides: each capture's soft bits per phase wait to be
  applied, at most workers + 2 results of up to four phases of the frame's bits, about 2 MB a
  phase at the 4K grid in modulation 1 and up to 7.7 MB at the largest grid in modulation 3;
  detection and fitting before the gate, about 46 MiB live and 85 MiB allocated for a capture of
  the largest grid; and each worker's geometry cache, up to two geometries of 22 MiB each at the
  largest grid (reviewer's measurements).
  **M6, scale work (designer's ruling (b)):** cut the stripe cancellation's allocations, about
  200 MiB a 4K capture (reviewer's figures above), which would let more captures share the stage.
  **Throughput (a note, not a merge condition):** 60 captures of the 4K output, 16 CPUs, decoded
  in 13.0 s with 2,870 MiB of heap growth before the gate (12 workers, the stage unbounded) and in
  27.3 s with 861 MiB after (6 workers, one capture in the stage at a time). **Engine memory:** the
  budget assumes the engine decodes in its worker processes; a job that falls back to running in
  process puts its 512 MiB reservation in the Go heap besides (SECURITY.md section 5), not
  re-budgeted. **`FuzzDecode`, for M7:** slow and of limited reach as built: 46 executions in 45 s
  on the development machine, each a whole decode of four 480p frames; it patches only the
  brightness plane; it has no committed corpus beyond its seeds. **Frame
  headers:** `frame.DecodeHeader` refuses a header without saying why (a CRC or a `Validate`
  failure); the reason is a candidate for M4's `inspect`, its first consumer (designer's ruling).
  The real-device check took 210-500 s per recording (about 2,300-4,800 captures at up to 4K, 16 threads; the public decode of recording
  1, 255 s); a round-trip matrix row takes 3-14 s alone.
- **Main red at df8e9b5: `TestWatchdogUnderLoad` (2026-11-18).** Main's push run 44400775718 failed
  in the check job on macos-26-intel, step "Race tests, isolated packages (Unix)": in the capped
  case (5 instances in 3 slots) one instance was cancelled 6.0008 s after its turn plus the 2 s
  limit, against 5 s. The pull request behind the run changed nothing in `internal/video`. Cause:
  the test measured from the instance's turn, while the stall clock starts when the instance's
  512 MiB of linear memory has been reserved (section 12), so the turn-to-reservation phase counted
  against the margin. Every instance returned at most 75 ms after its cancellation (observed). The
  figure, a whole number of seconds plus under 1 ms, is consistent with a cancellation within 1 ms
  of a tick after a reservation of 5 to 6 s; that split, and the tick timing, are inferred, since
  the run logged no per-instance breakdown. Locally, pinned to 4 and 2 CPUs with and without the
  race detector and background load (165 runs of the test), the overshoot measured from the turn was always
  the turn-to-reservation time plus up to one 1 s tick; the timers were on time in the capped case
  (median 1 ms late) and wazero reached its cancellation check within 23 ms. The test now measures
  from the reservation, with the same 5 s for race and non-race builds, logs the turn-to-reservation
  time, and logs the instance's breakdown on a late cancellation. The phase before the reservation
  stays bounded by `EngineLoadLimit` (`TestReservationLoadLimit`, which now also checks that the
  slot is released); no guest code runs and no input is read in it, so a hostile file cannot
  stretch it.
- **Performance, noted and not tuned: the reservation tail and the 1 s tick.** The 512 MiB
  reservation (a Go `make`) usually takes 35 to 70 ms, but under contention it reached 4.74 s
  locally (2 CPUs, race, background load, in the uncapped case) and 4.4 s in the capped case (4
  CPUs, non-race, other loads running), and it is inferred at 5 to 6 s on macos-26-intel in run
  44400775718; the inferred figure is unverified until the first CI run that logs the breakdown.
  The cause of the tail is not established (stop-the-world pauses were at most 1 ms in a traced
  run). With the 1 s watchdog tick and a stall clock that starts at the reservation, which is not
  aligned to a tick, an instance is cancelled between the limit and the limit plus 1 s after its
  reservation.
- **`scripts/setup.sh` hung on apt (2026-11-19).** On PR #52's CI run 44474815418 the setup step
  stalled in apt on a package mirror until the step's 35-minute limit; one infrastructure rerun was
  allowed by the designer's ruling. **Assigned to M4's first pull request, the tooling one:**
  `apt-get update` and `install` get Acquire timeouts and retries, and every package-manager command
  a per-command timeout, so that a mirror stall fails within a few minutes with a clear message;
  the same for any dnf, pacman, brew or winget step that can hang.

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

Each change to this document is one file in `docs/changes/DESIGN/`, named `YYYY-MM-DD-<topic>.md`
(the date in UTC); read in name order (by topic within a day), the files are the log, starting with
`2026-11-13-table.md`, the table of the entries up to 2026-11-15. Add an entry with `go run
./tools/dev changelog add DESIGN <topic> "<text>"` and read the log with `go run ./tools/dev
changelog show DESIGN` (DESIGN.md section 17).
