#!/bin/sh
# Regenerates the committed test clips of internal/video with a native ffmpeg that has libx264 and
# libx265 (made with ffmpeg 6.1.1). The clips are synthetic: random grey cells, or a flat grey picture.
# Run from this directory: sh gen.sh
set -e
FF="ffmpeg -nostdin -hide_banner -loglevel error -y"
# 8-pixel cells of random grey, a new pattern every frame, 384x216.
SRC="color=gray:s=48x27:r=30,noise=alls=100:allf=t+u,scale=384:216:flags=neighbor"
X265="keyint=12:bframes=3:log-level=error:range=limited"

# HEVC Main, BT.709, 24 frames with B-frames (decode order differs from output order).
$FF -f lavfi -i "$SRC,format=yuv420p" -frames:v 24 -c:v libx265 -preset fast -crf 30 -tag:v hvc1 \
  -x265-params "$X265:colorprim=bt709:transfer=bt709:colormatrix=bt709" \
  -color_range tv -colorspace bt709 -color_primaries bt709 -color_trc bt709 hevc8.mp4

# HEVC Main 10, HLG (arib-std-b67), BT.2020, 24 frames.
$FF -f lavfi -i "$SRC,format=yuv420p10le" -frames:v 24 -c:v libx265 -preset fast -crf 30 -tag:v hvc1 \
  -x265-params "$X265:colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc" \
  -color_range tv -colorspace bt2020nc -color_primaries bt2020 -color_trc arib-std-b67 hevc10hlg.mp4

# HEVC 4:4:4, 64x64, 4 frames: refused by the fast path's parameter-set check.
$FF -f lavfi -i "color=gray:s=64x64:r=30,noise=alls=60:allf=t+u,format=yuv444p" -frames:v 4 \
  -c:v libx265 -preset fast -crf 30 -x265-params "log-level=error" hevc444.mp4

# A small file of many skip pictures: 600 frames of flat grey at 3840x2160 (20 s at 30 fps)
# in about 36 KB, refused by MaxDecodedPixelsPerInputByte. Matroska, so that a test can rewrite its
# Duration element to make the container lie.
$FF -f lavfi -i "color=gray:s=3840x2160:r=30" -frames:v 600 -c:v libx264 -preset ultrafast -qp 51 \
  -g 100000 -bf 0 skip.mkv
ls -l hevc8.mp4 hevc10hlg.mp4 hevc444.mp4 skip.mkv
