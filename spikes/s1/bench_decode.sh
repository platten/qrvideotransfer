#!/bin/sh
# S1 decode measurements. Run from spikes/s1 after genclips.sh.
# Each line is a separate process so that peak RSS is per measurement.
S=./out/s1
for clip in "out/c1080p30_h264.mp4 0" "out/c4k60_hevc8.mp4 120" "out/c4k60_hevc10_hlg.mp4 120"; do
  set -- $clip
  for v in "" "-luma" "-skiplf" "-luma -skiplf"; do
    $S decode -in $1 -frames $2 $v | grep RESULT
    $S decode -native -in $1 -frames $2 $v | grep RESULT
  done
  for n in 2 4 8 16; do
    $S decode -in $1 -frames $2 -luma -n $n | grep RESULT
  done
done
