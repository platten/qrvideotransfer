package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/gen2brain/h265/hevc"
)

// cmdH265 decodes an Annex B HEVC elementary stream with the pure-Go
// github.com/gen2brain/h265 decoder (the DESIGN.md section 12 fallback),
// single-threaded, and reports fps. With ref, it compares the luma of every
// decoded frame with a raw gray (or gray10le) file from another decoder.
func cmdH265(in, ref string, maxFrames, threads int) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var refData []byte
	if ref != "" {
		if refData, err = os.ReadFile(ref); err != nil {
			return err
		}
	}
	nals := hevc.SplitAnnexB(data)
	var d hevc.Decoder
	d.Threads(threads)
	frames, mismatched := 0, 0
	var refOff int
	check := func(p *hevc.Picture) {
		defer p.Release()
		if frames >= maxFrames {
			return
		}
		frames++
		if refData == nil {
			return
		}
		var y []byte
		if p.BitDepth > 8 {
			y = make([]byte, 0, 2*p.CropW*p.CropH)
			for r := 0; r < p.CropH; r++ {
				row := p.Y16[(p.CropY+r)*p.StrideY+p.CropX:][:p.CropW]
				for _, v := range row {
					y = binary.LittleEndian.AppendUint16(y, v)
				}
			}
		} else {
			y = make([]byte, 0, p.CropW*p.CropH)
			for r := 0; r < p.CropH; r++ {
				y = append(y, p.Y[(p.CropY+r)*p.StrideY+p.CropX:][:p.CropW]...)
			}
		}
		if refOff+len(y) > len(refData) || !bytes.Equal(y, refData[refOff:refOff+len(y)]) {
			mismatched++
		}
		refOff += len(y)
	}
	start := time.Now()
	for _, n := range nals {
		pics, err := d.DecodeNAL(n)
		if err != nil {
			return err
		}
		for _, p := range pics {
			check(p)
		}
		if frames >= maxFrames {
			break
		}
	}
	if frames < maxFrames {
		for _, p := range d.Flush() {
			check(p)
		}
	}
	el := time.Since(start).Seconds()
	_, hwm := rss()
	sum := sha256.Sum256(data)
	fmt.Printf("RESULT h265 %s (sha256 %x): frames=%d threads=%d fps=%.2f mismatched-vs-ref=%d (ref=%v) process-peak-rss=%.0fMB\n",
		in, sum[:4], frames, threads, float64(frames)/el, mismatched, ref != "", mb(hwm))
	return nil
}
