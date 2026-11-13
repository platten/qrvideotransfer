package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// verify checks pattern file mp4 against the plan: container and stream
// properties, then, for the first, middle and last code frame of every
// segment (every video frame showing them), the header and every data
// cell, anchor cell and structure element. Decoding uses the system
// ffmpeg; H.264 decoding is exact, so any conforming decoder gives the
// same samples.
func verify(ctx context.Context, pl *plan, painters []*painter, mp4 string) error {
	if err := verifyContainer(ctx, pl, mp4); err != nil {
		return err
	}
	sampled := map[uint32]bool{}
	for _, s := range pl.Segments {
		for _, k := range []int{0, s.CodeFrames / 2, s.CodeFrames - 1} {
			sampled[s.FirstIndex+uint32(k)] = true //nolint:gosec // small
		}
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-noautorotate", "-i", mp4,
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Start(); err != nil {
		return err
	}
	fs := painters[0].frameSize()
	got, want := make([]byte, fs), make([]byte, fs)
	lastSeg, lastIdx := -1, uint32(0)
	type stat struct{ frames, checked, bad, headerBad int }
	stats := make([]stat, len(pl.Segments))
	v := 0
	for ; ; v++ {
		if _, err := io.ReadFull(stdout, got); err != nil {
			break
		}
		if v >= pl.VideoFrames {
			continue
		}
		si, idx := pl.videoFrame(v)
		if !sampled[idx] {
			continue
		}
		pt := painters[si]
		if si != lastSeg || idx != lastIdx {
			pt.render(idx, want)
			lastSeg, lastIdx = si, idx
		}
		n, bad, hdrOK := pt.compare(idx, got, want)
		st := &stats[si]
		st.frames++
		st.checked += n
		st.bad += bad
		if !hdrOK {
			st.headerBad++
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("decode: %v %s", err, eb.String())
	}
	if v != pl.VideoFrames {
		return fmt.Errorf("decoded %d video frames, want %d", v, pl.VideoFrames)
	}
	failed := 0
	for i, s := range pl.Segments {
		st := stats[i]
		fmt.Printf("  segment %2d rate %2d cell %2d mod %d: %d video frames checked, %d points, %d wrong, %d headers wrong\n",
			i, s.Rate, s.Cell, s.Modulation, st.frames, st.checked, st.bad, st.headerBad)
		if st.bad != 0 || st.headerBad != 0 || st.frames == 0 {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s: %d segments failed verification", mp4, failed)
	}
	fmt.Printf("VERIFIED %s: %d video frames, F = %d\n", mp4, pl.VideoFrames, pl.TotalFrames)
	return nil
}

// verifyContainer checks FORMAT.md section 3 with ffprobe: one H.264
// video stream, no audio, yuv420p, BT.709 limited range, 60 fps, every
// frame an I key frame, and no edit list.
func verifyContainer(ctx context.Context, pl *plan, mp4 string) error {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,profile,pix_fmt,color_range,color_space,color_primaries,color_transfer,r_frame_rate,width,height,nb_frames",
		"-of", "json", mp4).Output()
	if err != nil {
		return fmt.Errorf("ffprobe: %w", err)
	}
	var j struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(out, &j); err != nil {
		return err
	}
	if len(j.Streams) != 1 {
		return fmt.Errorf("%d streams, want 1", len(j.Streams))
	}
	s := j.Streams[0]
	want := map[string]any{"codec_type": "video", "codec_name": "h264", "pix_fmt": "yuv420p", "color_range": "tv",
		"color_space": "bt709", "color_primaries": "bt709", "color_transfer": "bt709", "r_frame_rate": "60/1",
		"width": float64(pl.Width), "height": float64(pl.Height), "nb_frames": fmt.Sprint(pl.VideoFrames)}
	for k, v := range want {
		if s[k] != v {
			return fmt.Errorf("stream %s = %v, want %v", k, s[k], v)
		}
	}
	fr, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
		"frame=pict_type,key_frame", "-of", "csv=p=0", mp4).Output()
	if err != nil {
		return fmt.Errorf("ffprobe frames: %w", err)
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(fr)), "\n") {
		if !strings.HasPrefix(l, "1,I") {
			return fmt.Errorf("frame %d is %q, want an I key frame", n, l)
		}
		n++
	}
	if n != pl.VideoFrames {
		return fmt.Errorf("%d frames listed, want %d", n, pl.VideoFrames)
	}
	b, err := os.ReadFile(mp4)
	if err != nil {
		return err
	}
	if bytes.Contains(b[:min(len(b), 1<<20)], []byte("elst")) {
		return fmt.Errorf("the MP4 has an edit list")
	}
	fmt.Printf("  container: h264 %v, yuv420p, tv, bt709, 60/1, %d frames all I/key, no edit list\n", s["profile"], n)
	return nil
}

// compare checks one decoded frame against the expected rendering of
// code frame idx at the centre of every data cell, anchor cell and
// structure element, classifying both to the nearest colour of the
// modulation (or mid-grey); it also decodes the header from the slots. It
// returns the number of points checked and of points that differ.
func (pt *painter) compare(idx uint32, got, want []byte) (int, int, bool) {
	palette := append([]ycc{midGrey}, pt.levels...)
	at := func(buf []byte, x, y int) ycc {
		cw := pt.w / 2
		u := buf[pt.w*pt.h:]
		v := u[cw*(pt.h/2):]
		return ycc{buf[y*pt.w+x], u[(y/2)*cw+x/2], v[(y/2)*cw+x/2]}
	}
	class := func(c ycc) int {
		best, bd := 0, 1<<30
		for i, p := range palette {
			d := 0
			for k := range 3 {
				e := int(c[k]) - int(p[k])
				d += e * e
			}
			if d < bd {
				best, bd = i, d
			}
		}
		return best
	}
	n, bad := 0, 0
	check := func(x, y int) {
		n++
		if class(at(got, x, y)) != class(at(want, x, y)) {
			bad++
		}
	}
	p := pt.p
	for _, c := range pt.layout.DataCells() {
		check(int(c.X)*p+p/2, int(c.Y)*p+p/2)
	}
	for _, a := range pt.layout.Anchors() {
		for dy := range frame.AnchorH {
			for dx := range frame.AnchorW {
				check((a.X+dx)*p+p/2, (a.Y+dy)*p+p/2)
			}
		}
	}
	centre := func(r image.Rectangle) (int, int) { return (r.Min.X + r.Max.X) / 2, (r.Min.Y + r.Max.Y) / 2 }
	es := pt.structure(idx)
	var finders [4]image.Rectangle
	soft := make([]int, frame.HeaderSlots)
	slot, corner := 0, -1
	dark, light := int(pt.levels[0][0]), int(pt.levels[len(pt.levels)-1][0])
	for _, el := range es {
		switch el.kind {
		case kindSeparator:
			corner++
		case kindBorder:
			// Sample the band's centre line every 8 cells.
			x, y := centre(el.r)
			if el.r.Dx() > el.r.Dy() {
				for x = el.r.Min.X + 4*p; x < el.r.Max.X; x += 8 * p {
					check(x, y)
				}
			} else {
				for y = el.r.Min.Y + 4*p; y < el.r.Max.Y; y += 8 * p {
					check(x, y)
				}
			}
		case kindFinder:
			finders[corner] = finders[corner].Union(el.r)
			check(centre(el.r))
		case kindSlot:
			x, y := centre(el.r)
			check(x, y)
			soft[slot%frame.HeaderSlots] += (dark + light) - 2*int(got[y*pt.w+x])
			slot++
		case kindPatch:
			check(centre(el.r))
		}
	}
	// Separators: the strips of [Uy, 9 Uy)^2 outside the finder.
	for c, el := range separators(es) {
		f := finders[c]
		r := el.r
		for _, s := range []image.Rectangle{
			image.Rect(f.Max.X, r.Min.Y, r.Max.X, r.Max.Y), image.Rect(r.Min.X, r.Min.Y, f.Min.X, r.Max.Y),
			image.Rect(r.Min.X, f.Max.Y, r.Max.X, r.Max.Y), image.Rect(r.Min.X, r.Min.Y, r.Max.X, f.Min.Y),
		} {
			if s.Dx() > 0 && s.Dy() > 0 {
				check(centre(s))
			}
		}
	}
	llr := make([]int8, frame.HeaderSlots)
	for i, s := range soft {
		llr[i] = int8(max(-127, min(127, s/8)))
	}
	h, ok := frame.DecodeHeader(llr)
	return n, bad, ok && h == pt.header(idx)
}

func separators(es []elem) []elem {
	var s []elem
	for _, e := range es {
		if e.kind == kindSeparator {
			s = append(s, e)
		}
	}
	return s
}
