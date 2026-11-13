#!/bin/sh
# Repeated decode runs (single and 4 instances, interleaved) with the wasm
# memory reserved up front (-pages 8192 = 512 MiB per instance). WSL2
# moves vCPUs between the host's performance and efficiency cores, so
# single runs vary by +-25%; report medians. Run from spikes/s1.
S=./out/s1
for k in 1 2 3; do
  $S decode -pages 8192 -in out/c1080p30_h264.mp4 -luma | grep RESULT
  $S decode -pages 8192 -in out/c1080p30_h264.mp4 -luma -n 4 | grep RESULT
  $S decode -pages 8192 -in out/c4k60_hevc8.mp4 -frames 120 -luma | grep RESULT
  $S decode -pages 8192 -in out/c4k60_hevc8.mp4 -frames 120 -luma -n 4 | grep RESULT
  $S decode -pages 8192 -in out/c4k60_hevc8.mp4 -frames 120 -luma -skiplf | grep RESULT
  $S decode -pages 8192 -in out/c4k60_hevc10_hlg.mp4 -frames 120 -luma | grep RESULT
  $S decode -pages 8192 -in out/c4k60_hevc10_hlg.mp4 -frames 120 -luma -n 4 | grep RESULT
  $S decode -native -in out/c4k60_hevc8.mp4 -frames 120 -luma | grep RESULT
  $S decode -native -in out/c1080p30_h264.mp4 -luma | grep RESULT
done
