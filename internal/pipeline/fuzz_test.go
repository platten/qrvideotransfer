package pipeline

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// FuzzDecode is the whole decode from arbitrary raw frames (SECURITY.md section 9.1). The input
// alters a recording of a small transfer's four frames: its first byte picks a frame, the rest is
// patches of four bytes each, a position in that frame's brightness plane (big-endian, 24 bits)
// and the value to put there, at most 256 patches. The empty input leaves the recording perfect,
// and the seeds alter the header, a finder, the data and an anchor. Must hold: no panic; a decode
// that succeeds writes exactly the transfer's files; nothing is written outside the output folder
// and the temporary folder, and the temporary folder is left empty.
func FuzzDecode(f *testing.F) {
	in, want := writeInputs(f, 50, 2500)
	path, res := encodeRaw(f, profile480(in))
	frames, info := readFrames(f, video.NewRaw(), path)
	if res.Frames != len(frames) || len(frames) != 4 {
		f.Fatalf("%d frames, F %d", len(frames), res.Frames)
	}
	w := frames[0].W
	patch := func(frame byte, ps ...[2]int) []byte {
		b := []byte{frame}
		for _, p := range ps {
			b = binary.BigEndian.AppendUint32(b, uint32(p[0])<<8|uint32(p[1])&0xff)
		}
		return b
	}
	f.Add([]byte{})
	f.Add(patch(0, [2]int{40*w + 100, 255}, [2]int{41*w + 100, 0}))   // a header slot
	f.Add(patch(1, [2]int{12*w + 12, 16}, [2]int{20*w + 20, 235}))    // a finder
	f.Add(patch(2, [2]int{240*w + 424, 0}, [2]int{241*w + 424, 255})) // data cells
	f.Add(patch(3, [2]int{52*w + 48, 255}))                           // an anchor's corner
	f.Fuzz(func(t *testing.T, b []byte) {
		recording := make([]*video.Frame, len(frames))
		copy(recording, frames)
		if len(b) > 0 {
			i := int(b[0]) % len(frames)
			fr := *frames[i]
			fr.Y = append([]uint16(nil), fr.Y...)
			for k, rest := 0, b[1:]; k < 256 && len(rest) >= 4; k, rest = k+1, rest[4:] {
				v := binary.BigEndian.Uint32(rest)
				fr.Y[int(v>>8)%len(fr.Y)] = uint16(v & 0xff)
			}
			recording[i] = &fr
		}
		dir := t.TempDir()
		rec := writeRawIn(t, dir, recording, info.FPSNum/info.FPSDen)
		tmp, out := filepath.Join(dir, "tmp"), filepath.Join(dir, "out")
		if err := os.Mkdir(tmp, 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{rec}, Backend: video.NewRaw(), Workers: 2, TempDir: tmp,
			Unpack: payload.UnpackOptions{OutputDir: out}})
		if err == nil {
			checkOutput(t, out, want)
		} else if fs := filesUnder(t, out); len(fs) != 0 {
			t.Fatalf("a failed decode (%v) wrote %v", err, fs)
		}
		if fs := filesUnder(t, tmp); len(fs) != 0 {
			t.Fatalf("temporary files left: %v", fs)
		}
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if n := e.Name(); n != "tmp" && n != "out" && n != filepath.Base(rec) {
				t.Fatalf("written beside the output folder: %s", n)
			}
		}
	})
}
