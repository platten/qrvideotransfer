#!/bin/sh
# Faster x264 intra settings, with display bitrate and cell errors. Run from spikes/s1.
S=./out/s1
for x in "" ":no-deblock=1" ":no-deblock=1:aq-mode=0:psy=0:trellis=0" ":no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:no-8x8dct=1"; do
  $S encode -w 1920 -h 1080 -frames 30 -preset veryfast -x264 "$x" -check -out out/f1080.mp4 | grep -E "RESULT"
  $S encode -w 3840 -h 2160 -frames 10 -preset veryfast -x264 "$x" -out out/f4k.mp4 | grep RESULT
done
$S encode -w 3840 -h 2160 -frames 10 -preset ultrafast -x264 ":no-deblock=1" -check -out out/u4k.mp4 | grep RESULT
$S encode -w 1920 -h 1080 -frames 30 -preset ultrafast -x264 ":no-deblock=1" -check -out out/u1080.mp4 | grep RESULT
