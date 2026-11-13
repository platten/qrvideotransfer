#!/bin/sh
# Writes and verifies the four S2 pattern files into spikes/pattern/out/.
# Run from the repository root. Needs the system ffmpeg/ffprobe for verification only.
set -e
go build -o spikes/pattern/out/pattern ./spikes/pattern
for size in "1920 1080" "2560 1440" "3200 2000" "3840 2160"; do
  set -- $size
  spikes/pattern/out/pattern -w $1 -h $2 -workers ${WORKERS:-10} -out spikes/pattern/out
done
