# S2 test pattern

`spikes/pattern` writes `pattern-<W>x<H>.mp4`, the test video for step S2 (docs/IMPLEMENTATION_PROMPT.md
section 5): real code frames of docs/FORMAT.md whose data cells carry no codewords, so that an analysis
can recompute every expected symbol from each frame's header. It stays in the repository until the S2
recordings have been analysed.

## What each file contains

- One H.264 MP4 at 60 fps, as FORMAT.md section 3: every frame IDR, CABAC, BT.709 limited range,
  `yuv420p` built by the program (never converted by ffmpeg), no audio, no edit list. Encoded by the
  embedded engine (go-ffmpreg under wazero, as in spike S1) with x264 at quantiser 32 and the settings of
  DESIGN.md section 12 (`-preset veryfast`, `no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:
  no-8x8dct=1`), so the stream is Main profile. Workers encode runs of 120 frames (an even number, so
  that `idr_pic_id` alternates across the joins) to elementary streams, which are concatenated and
  wrapped in MP4 by one copy pass.
- Segments of 4 s, in this order: for each display rate 12, 30, 60 fps; for each cell size 4, 8 and,
  where it divides both W and H, 12 pixels; for each modulation 1 (Gray2), 2 (Gray4), 3 (RGB8).
- Display rate r: each code frame is shown for 60 / r video frames.
- Every code frame is a complete FORMAT.md frame: border, four finders (the bottom-right one inverted),
  separators, four header copies, four palette strips, anchors with phase markers, data cells. Default
  levels of FORMAT.md section 7; painted and rasterised as section 4.5.
- Header: session `0x5332`; frame index counting code frames from 0 across the whole file; F = the total
  code-frame count rounded up to a multiple of 4 (the last segment is lengthened if needed; with 4 s
  segments every count is already a multiple of 4); the segment's grid and modulation; inner code 2.
- Data cells: every bit position holds its scramble bit alone (FORMAT.md section 9 with all codeword bits
  zero), so the symbol of data cell e in frame f is the b scramble bits at positions e b .. e b + b - 1.
- Beside each MP4, `pattern-<W>x<H>.json` holds the same segment table (start time, rate, cell, grid,
  modulation, first frame index, code frames, first video frame).

## Verification

After writing, the program checks the file and stops with an error on any difference:

- ffprobe: one stream, H.264, `yuv420p`, `tv`, BT.709 primaries, transfer and matrix, 60/1, the expected
  frame count, every frame an I key frame; no `elst` box.
- It decodes the whole file (system ffmpeg; H.264 decoding is exact, so every conforming decoder gives
  the same samples) and, for the first, middle and last code frame of every segment, at every video
  frame that shows them: classifies the centre pixel of every data cell, every anchor cell, every finder
  module, every separator strip, every header slot, every palette patch and points every 8 cells along
  the border to the nearest default colour (or mid-grey), and requires the class to equal that of the
  frame the program rendered; and decodes the header from the four copies with `frame.DecodeHeader`,
  requiring every field to equal the expected header.

All three files passed: zero wrong points and zero wrong headers in every segment.

## Reproduce

From the repository root (needs the system `ffmpeg` and `ffprobe` for verification only):

```sh
sh spikes/pattern/generate.sh          # WORKERS=10 by default
go run ./spikes/pattern -table -w 1920 -h 1080
go run ./spikes/pattern -verify-only -w 3840 -h 2160 -out spikes/pattern/out
```

## Segment tables

`pattern-1920x1080.mp4`: 6480 video frames (108.0 s), F = 3672 code frames.

| # | Start (s) | Rate (fps) | Cell (px) | Grid | Modulation | First frame index | Code frames |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 0.0 | 12 | 4 | 480 x 270 | 1 Gray2 | 0 | 48 |
| 1 | 4.0 | 12 | 4 | 480 x 270 | 2 Gray4 | 48 | 48 |
| 2 | 8.0 | 12 | 4 | 480 x 270 | 3 RGB8 | 96 | 48 |
| 3 | 12.0 | 12 | 8 | 240 x 135 | 1 Gray2 | 144 | 48 |
| 4 | 16.0 | 12 | 8 | 240 x 135 | 2 Gray4 | 192 | 48 |
| 5 | 20.0 | 12 | 8 | 240 x 135 | 3 RGB8 | 240 | 48 |
| 6 | 24.0 | 12 | 12 | 160 x 90 | 1 Gray2 | 288 | 48 |
| 7 | 28.0 | 12 | 12 | 160 x 90 | 2 Gray4 | 336 | 48 |
| 8 | 32.0 | 12 | 12 | 160 x 90 | 3 RGB8 | 384 | 48 |
| 9 | 36.0 | 30 | 4 | 480 x 270 | 1 Gray2 | 432 | 120 |
| 10 | 40.0 | 30 | 4 | 480 x 270 | 2 Gray4 | 552 | 120 |
| 11 | 44.0 | 30 | 4 | 480 x 270 | 3 RGB8 | 672 | 120 |
| 12 | 48.0 | 30 | 8 | 240 x 135 | 1 Gray2 | 792 | 120 |
| 13 | 52.0 | 30 | 8 | 240 x 135 | 2 Gray4 | 912 | 120 |
| 14 | 56.0 | 30 | 8 | 240 x 135 | 3 RGB8 | 1032 | 120 |
| 15 | 60.0 | 30 | 12 | 160 x 90 | 1 Gray2 | 1152 | 120 |
| 16 | 64.0 | 30 | 12 | 160 x 90 | 2 Gray4 | 1272 | 120 |
| 17 | 68.0 | 30 | 12 | 160 x 90 | 3 RGB8 | 1392 | 120 |
| 18 | 72.0 | 60 | 4 | 480 x 270 | 1 Gray2 | 1512 | 240 |
| 19 | 76.0 | 60 | 4 | 480 x 270 | 2 Gray4 | 1752 | 240 |
| 20 | 80.0 | 60 | 4 | 480 x 270 | 3 RGB8 | 1992 | 240 |
| 21 | 84.0 | 60 | 8 | 240 x 135 | 1 Gray2 | 2232 | 240 |
| 22 | 88.0 | 60 | 8 | 240 x 135 | 2 Gray4 | 2472 | 240 |
| 23 | 92.0 | 60 | 8 | 240 x 135 | 3 RGB8 | 2712 | 240 |
| 24 | 96.0 | 60 | 12 | 160 x 90 | 1 Gray2 | 2952 | 240 |
| 25 | 100.0 | 60 | 12 | 160 x 90 | 2 Gray4 | 3192 | 240 |
| 26 | 104.0 | 60 | 12 | 160 x 90 | 3 RGB8 | 3432 | 240 |

`pattern-2560x1440.mp4`: 4320 video frames (72.0 s), F = 2448 code frames.

| # | Start (s) | Rate (fps) | Cell (px) | Grid | Modulation | First frame index | Code frames |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 0.0 | 12 | 4 | 640 x 360 | 1 Gray2 | 0 | 48 |
| 1 | 4.0 | 12 | 4 | 640 x 360 | 2 Gray4 | 48 | 48 |
| 2 | 8.0 | 12 | 4 | 640 x 360 | 3 RGB8 | 96 | 48 |
| 3 | 12.0 | 12 | 8 | 320 x 180 | 1 Gray2 | 144 | 48 |
| 4 | 16.0 | 12 | 8 | 320 x 180 | 2 Gray4 | 192 | 48 |
| 5 | 20.0 | 12 | 8 | 320 x 180 | 3 RGB8 | 240 | 48 |
| 6 | 24.0 | 30 | 4 | 640 x 360 | 1 Gray2 | 288 | 120 |
| 7 | 28.0 | 30 | 4 | 640 x 360 | 2 Gray4 | 408 | 120 |
| 8 | 32.0 | 30 | 4 | 640 x 360 | 3 RGB8 | 528 | 120 |
| 9 | 36.0 | 30 | 8 | 320 x 180 | 1 Gray2 | 648 | 120 |
| 10 | 40.0 | 30 | 8 | 320 x 180 | 2 Gray4 | 768 | 120 |
| 11 | 44.0 | 30 | 8 | 320 x 180 | 3 RGB8 | 888 | 120 |
| 12 | 48.0 | 60 | 4 | 640 x 360 | 1 Gray2 | 1008 | 240 |
| 13 | 52.0 | 60 | 4 | 640 x 360 | 2 Gray4 | 1248 | 240 |
| 14 | 56.0 | 60 | 4 | 640 x 360 | 3 RGB8 | 1488 | 240 |
| 15 | 60.0 | 60 | 8 | 320 x 180 | 1 Gray2 | 1728 | 240 |
| 16 | 64.0 | 60 | 8 | 320 x 180 | 2 Gray4 | 1968 | 240 |
| 17 | 68.0 | 60 | 8 | 320 x 180 | 3 RGB8 | 2208 | 240 |

`pattern-3840x2160.mp4`: 6480 video frames (108.0 s), F = 3672 code frames.

| # | Start (s) | Rate (fps) | Cell (px) | Grid | Modulation | First frame index | Code frames |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 0.0 | 12 | 4 | 960 x 540 | 1 Gray2 | 0 | 48 |
| 1 | 4.0 | 12 | 4 | 960 x 540 | 2 Gray4 | 48 | 48 |
| 2 | 8.0 | 12 | 4 | 960 x 540 | 3 RGB8 | 96 | 48 |
| 3 | 12.0 | 12 | 8 | 480 x 270 | 1 Gray2 | 144 | 48 |
| 4 | 16.0 | 12 | 8 | 480 x 270 | 2 Gray4 | 192 | 48 |
| 5 | 20.0 | 12 | 8 | 480 x 270 | 3 RGB8 | 240 | 48 |
| 6 | 24.0 | 12 | 12 | 320 x 180 | 1 Gray2 | 288 | 48 |
| 7 | 28.0 | 12 | 12 | 320 x 180 | 2 Gray4 | 336 | 48 |
| 8 | 32.0 | 12 | 12 | 320 x 180 | 3 RGB8 | 384 | 48 |
| 9 | 36.0 | 30 | 4 | 960 x 540 | 1 Gray2 | 432 | 120 |
| 10 | 40.0 | 30 | 4 | 960 x 540 | 2 Gray4 | 552 | 120 |
| 11 | 44.0 | 30 | 4 | 960 x 540 | 3 RGB8 | 672 | 120 |
| 12 | 48.0 | 30 | 8 | 480 x 270 | 1 Gray2 | 792 | 120 |
| 13 | 52.0 | 30 | 8 | 480 x 270 | 2 Gray4 | 912 | 120 |
| 14 | 56.0 | 30 | 8 | 480 x 270 | 3 RGB8 | 1032 | 120 |
| 15 | 60.0 | 30 | 12 | 320 x 180 | 1 Gray2 | 1152 | 120 |
| 16 | 64.0 | 30 | 12 | 320 x 180 | 2 Gray4 | 1272 | 120 |
| 17 | 68.0 | 30 | 12 | 320 x 180 | 3 RGB8 | 1392 | 120 |
| 18 | 72.0 | 60 | 4 | 960 x 540 | 1 Gray2 | 1512 | 240 |
| 19 | 76.0 | 60 | 4 | 960 x 540 | 2 Gray4 | 1752 | 240 |
| 20 | 80.0 | 60 | 4 | 960 x 540 | 3 RGB8 | 1992 | 240 |
| 21 | 84.0 | 60 | 8 | 480 x 270 | 1 Gray2 | 2232 | 240 |
| 22 | 88.0 | 60 | 8 | 480 x 270 | 2 Gray4 | 2472 | 240 |
| 23 | 92.0 | 60 | 8 | 480 x 270 | 3 RGB8 | 2712 | 240 |
| 24 | 96.0 | 60 | 12 | 320 x 180 | 1 Gray2 | 2952 | 240 |
| 25 | 100.0 | 60 | 12 | 320 x 180 | 2 Gray4 | 3192 | 240 |
| 26 | 104.0 | 60 | 12 | 320 x 180 | 3 RGB8 | 3432 | 240 |
