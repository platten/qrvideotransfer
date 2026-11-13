#!/bin/sh
# Generates the three S1 test clips with the NATIVE ffmpeg into out/.
# Run from spikes/s1 after: CGO_ENABLED=0 go build -o out/s1 .
# Content: random four-level grey 5x5-pixel cells, new each frame, softened
# (Gaussian sigma 1) and given +-4 temporal noise, so the encoder spends its
# bitrate as on a real recording of a code. In the 4K60 clips each pattern is
# held for 2 frames (a 30 fps code captured at 60 fps): with a new pattern
# every frame x265 cannot get below about 70 Mbit/s even at its highest
# quantiser, so the rate control would not land on 53 Mbit/s.
set -e
mkdir -p out
VF="gblur=sigma=1,noise=alls=4:allf=t"
./out/s1 gen -w 1920 -h 1080 -cell 5 -frames 300 | ffmpeg -nostdin -hide_banner -loglevel error -y \
  -f rawvideo -pix_fmt yuv420p -s 1920x1080 -r 30 -i - -vf "$VF" \
  -c:v libx264 -preset medium -profile:v high -b:v 17M -maxrate 17M -bufsize 34M -g 30 \
  -color_range tv -colorspace bt709 -color_primaries bt709 -color_trc bt709 -pix_fmt yuv420p \
  out/c1080p30_h264.mp4 &
./out/s1 gen -w 3840 -h 2160 -cell 5 -frames 600 -hold 2 | ffmpeg -nostdin -hide_banner -loglevel error -y \
  -f rawvideo -pix_fmt yuv420p -s 3840x2160 -r 60 -i - -vf "$VF" \
  -c:v libx265 -preset fast -profile:v main -b:v 53M -maxrate 53M -bufsize 106M -tag:v hvc1 \
  -x265-params "keyint=60:log-level=error:colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited" \
  -color_range tv -colorspace bt709 -color_primaries bt709 -color_trc bt709 -pix_fmt yuv420p \
  out/c4k60_hevc8.mp4 &
./out/s1 gen -w 3840 -h 2160 -cell 5 -frames 600 -hold 2 | ffmpeg -nostdin -hide_banner -loglevel error -y \
  -f rawvideo -pix_fmt yuv420p -s 3840x2160 -r 60 -i - -vf "$VF,format=yuv420p10le" \
  -c:v libx265 -preset fast -profile:v main10 -b:v 53M -maxrate 53M -bufsize 106M -tag:v hvc1 \
  -x265-params "keyint=60:log-level=error:colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc:range=limited" \
  -color_range tv -colorspace bt2020nc -color_primaries bt2020 -color_trc arib-std-b67 -pix_fmt yuv420p10le \
  out/c4k60_hevc10_hlg.mp4 &
wait
ls -l out/*.mp4
