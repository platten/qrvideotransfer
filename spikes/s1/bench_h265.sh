#!/bin/sh
# gen2brain/h265 with and without its assembly (-tags noasm), one core,
# 120 frames of each 4K clip, interleaved runs. Run from spikes/s1.
set -e
CGO_ENABLED=0 go build -o out/s1 .
CGO_ENABLED=0 go build -tags noasm -o out/s1-noasm .
ffmpeg -v error -y -i out/c4k60_hevc8.mp4 -c copy -bsf:v hevc_mp4toannexb -f hevc out/c4k8.hevc
ffmpeg -v error -y -i out/c4k60_hevc10_hlg.mp4 -c copy -bsf:v hevc_mp4toannexb -f hevc out/c4k10.hevc
# Bit-exactness against the embedded engine's luma (first 120 frames).
./out/s1 ff -dir out -- -nostdin -loglevel error -i /d/c4k60_hevc8.mp4 -frames:v 120 -vf extractplanes=y -pix_fmt gray -f rawvideo -y /d/ref8.raw
./out/s1 ff -dir out -- -nostdin -loglevel error -i /d/c4k60_hevc10_hlg.mp4 -frames:v 120 -vf extractplanes=y -pix_fmt gray10le -f rawvideo -y /d/ref10.raw
./out/s1-noasm h265 -in out/c4k8.hevc -ref out/ref8.raw | sed 's/^RESULT/RESULT noasm-exact/'
./out/s1-noasm h265 -in out/c4k10.hevc -ref out/ref10.raw | sed 's/^RESULT/RESULT noasm-exact/'
for k in 1 2 3; do
  for c in c4k8 c4k10; do
    GOMAXPROCS=1 ./out/s1 h265 -in out/$c.hevc | sed 's/^RESULT/RESULT asm/'
    GOMAXPROCS=1 ./out/s1-noasm h265 -in out/$c.hevc | sed 's/^RESULT/RESULT noasm/'
  done
done
