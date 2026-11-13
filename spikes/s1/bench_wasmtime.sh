#!/bin/sh
# S1 phase 2: the same go-ffmpreg module under wasmtime's CLI and under wazero,
# same clips and arguments, one instance, interleaved runs. Run from spikes/s1
# after genclips.sh and building out/s1. WT is a scratch directory for the
# extracted module; WASMTIME the wasmtime binary.
set -e
WT=${WT:?set WT to a scratch directory}
WASMTIME=${WASMTIME:-wasmtime}
FEAT="-W exceptions=y,tail-call=y,simd=y,extended-const=y"
mkdir -p "$WT"
gunzip -c "$(go env GOMODCACHE)/codeberg.org/gruf/go-ffmpreg@v0.6.22/embed/ffmpreg.wasm.gz" > "$WT/ffmpreg.wasm"
$WASMTIME compile $FEAT "$WT/ffmpreg.wasm" -o "$WT/ffmpreg.cwasm"
OUT=$(pwd)/out

# wt clip frames framebytes [args...]: decode with wasmtime, print fps.
# The wall time includes wasmtime's start-up with the precompiled module.
wt() {
  clip=$1; n=$2; fb=$3; shift 3
  s=$(date +%s.%N)
  bytes=$($WASMTIME run $FEAT,unknown-imports-trap=y --allow-precompiled --dir "$OUT::/d" --argv0 ffmpeg "$WT/ffmpreg.cwasm" \
    -nostdin -hide_banner -loglevel error -noautorotate -i "/d/$clip" -frames:v "$n" "$@" -f rawvideo pipe:1 | wc -c)
  e=$(date +%s.%N)
  echo "$bytes $fb $s $e" | awk '{printf "%.2f (frames %d)\n", $1/$2/($4-$3), $1/$2}'
}

for k in 1 2 3; do
  for spec in "c1080p30_h264.mp4 300 1920 1080 gray" "c4k60_hevc8.mp4 120 3840 2160 gray" "c4k60_hevc10_hlg.mp4 120 3840 2160 gray10le"; do
    set -- $spec
    clip=$1; n=$2; w=$3; h=$4; lf=$5
    if [ "$lf" = gray ]; then lb=$((w*h)); fb=$((w*h*3/2)); else lb=$((2*w*h)); fb=$((3*w*h)); fi
    echo "RESULT wasmtime $clip luma fps=$(wt $clip $n $lb -vf extractplanes=y -pix_fmt $lf)"
    echo "RESULT wasmtime $clip full fps=$(wt $clip $n $fb)"
    ./out/s1 decode -pages 8192 -in out/$clip -frames $n -luma | grep RESULT | sed 's/.*aggregate fps=\([0-9.]*\).*/RESULT wazero '"$clip"' luma fps=\1/'
    ./out/s1 decode -pages 8192 -in out/$clip -frames $n | grep RESULT | sed 's/.*aggregate fps=\([0-9.]*\).*/RESULT wazero '"$clip"' full fps=\1/'
  done
done
