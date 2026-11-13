#!/bin/sh
# S1 encode, display-bitrate and HDR measurements. Run from spikes/s1.
S=./out/s1
# Speed per instance, x264 intra-only, CABAC, qp 32, by preset.
for p in medium veryfast superfast; do
  $S encode -w 1920 -h 1080 -frames 30 -preset $p -out out/e1080_$p.mp4 | grep RESULT
  $S encode -w 3840 -h 2160 -frames 10 -preset $p -out out/e4k_$p.mp4 | grep RESULT
done
# Parallel efficiency, 1080p, 4 instances.
$S encode -w 1920 -h 1080 -frames 30 -preset medium -n 4 -out out/epar.mp4 | grep RESULT
# Display bitrate and cell errors (decoding the engine's own output).
for m in "gray4 4" "gray4 8" "gray2 4" "rgb8 4" "rgb8 8"; do
  set -- $m
  $S encode -w 1920 -h 1080 -frames 30 -mod $1 -cell $2 -check -out out/b_$1_$2.mp4 | grep -E "RESULT|probe"
done
for qp in 28 34 36 38 40; do
  $S encode -w 1920 -h 1080 -frames 30 -mod gray4 -cell 4 -qp $qp -check -out out/b_q$qp.mp4 | grep RESULT
done
# HDR candidates: 10-bit eight-level grey (Gray8).
$S encode -w 1920 -h 1080 -frames 10 -mod gray8 -codec x264-10 -check -out out/h_x264_10.mp4 | grep -E "RESULT|probe"
$S encode -w 1920 -h 1080 -frames 10 -mod gray8 -codec aom10 -preset 8 -qp 20 -extra "-usage realtime" -check -out out/h_aom_rt8.mp4 | grep -E "RESULT|probe"
$S encode -w 1920 -h 1080 -frames 4 -mod gray8 -codec aom10 -preset 6 -qp 20 -check -out out/h_aom_good6.mp4 | grep -E "RESULT|probe"
