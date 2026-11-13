// Package qrvt is the public API of qrvt, which turns files into a looping video of on-screen codes
// and turns a camera recording of that video back into the files.
//
// The package is named qrvt although the module path ends in qrvideotransfer. It does no terminal
// I/O and never exits the process: progress is reported through typed [Event] values delivered to a
// callback, and cancellation is by context. See docs/DESIGN.md section 14.
package qrvt

import "context"

// Encode packs the inputs named in o and writes the code video.
func Encode(ctx context.Context, o EncodeOptions) (*EncodeReport, error) {
	return nil, ErrNotImplemented
}

// Decode recovers the files carried by one or more recordings of a code video.
func Decode(ctx context.Context, o DecodeOptions) (*DecodeReport, error) {
	return nil, ErrNotImplemented
}

// CalibrationVideo writes a test video that steps through modes, densities and display rates.
func CalibrationVideo(ctx context.Context, o CalibrationOptions) error {
	return ErrNotImplemented
}

// AnalyzeCalibration measures a recording of the calibration video and recommends a profile.
func AnalyzeCalibration(ctx context.Context, o AnalyzeOptions) (*Recommendation, error) {
	return nil, ErrNotImplemented
}

// Inspect reports what a code video or a recording of one carries, without unpacking it.
func Inspect(ctx context.Context, o InspectOptions) (*StreamInfo, error) {
	return nil, ErrNotImplemented
}

// GenerateKey returns a new Ed25519 signing key pair in qrvt's key-file encoding.
func GenerateKey() (public, private []byte, err error) {
	return nil, nil, ErrNotImplemented
}
