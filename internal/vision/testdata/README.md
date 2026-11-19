# internal/vision test data

## rawber.json

The references of the raw bit error rate sweeps (`rad_rawber_test.go`; DESIGN.md section 15, row
"Blur, noise, recorder quantiser"). Each entry is one point of a sweep: the preset (`tripod` or
`handheld`, camera resized to 1600 x 900), the parameter varied (`blur`, Gaussian sigma in camera
pixels; `noise`, sigma in 8-bit code values; `step`, the recorder stand-in's quantiser step in 8-bit
code values, 0 for lossless), its value, the captures filmed, and the integer counts of wrong and
credited bits. The bench session is a 212 x 120 four-grey grid, 8 random frames at 30 fps (seed
0x92), filmed at the camera's 60 fps (seed 0xb2) and read at true positions. The 36 sweep points
are filmed for 4 captures (`TestRawBERSweeps`, long only); the last entry, `tripod`'s own point
filmed for 1 capture, is the always-on regression check's own reference (`TestRawBERReference`).

Stored at the M3 exit (2026-11-19), from the code at that commit. The stages these points measure
(render, the simulator, the bench's sampler, the phase field, the equaliser and SoftBits) gave
identical counts at the merge of the last M2 pull request (973c897).

Regenerate, only when a change to those stages is intended and recorded in DESIGN.md:

    QRVT_UPDATE_RAWBER=1 go test ./internal/vision -run TestRawBERSweeps -count=1
