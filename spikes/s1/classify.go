package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// cmdClassify decodes a genclips.sh clip (luma only) and counts cells whose
// nearest grey differs from the generated symbol, per frame, to see what
// skipping the loop filter costs.
func cmdClassify(ctx context.Context, e *engine, in string, cell, hold, n int, skip bool) error {
	dir, name := splitPath(in)
	pi, err := e.probe(ctx, dir, name)
	if err != nil {
		return err
	}
	p := newPainter(pi.W, pi.H, cell, "gray4", 1)
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-noautorotate"}
	if skip {
		a = append(a, "-skip_loop_filter", "all")
	}
	a = append(a, "-i", "/d/"+name, "-frames:v", strconv.Itoa(n), "-vf", "extractplanes=y", "-pix_fmt", "gray", "-f", "rawvideo", "pipe:1")
	pr, pw := io.Pipe()
	go func() {
		e.Run(ctx, run{Name: "ffmpeg", Args: a, Dir: dir, RO: true, Stdout: pw})
		pw.Close()
	}()
	frame := make([]byte, pi.W*pi.H)
	total, cells := 0, p.gx*p.gy
	var per []string
	for f := 0; ; f++ {
		if _, err := io.ReadFull(pr, frame); err != nil {
			fmt.Printf("RESULT classify %s skiplf=%v: frames=%d cell error rate=%.5f; per frame: %s\n",
				name, skip, f, float64(total)/float64(max(f, 1)*cells), strings.Join(per, " "))
			return nil
		}
		k := p.classify(frame, p.symbols(uint64(f/hold)), true)
		total += k
		per = append(per, fmt.Sprintf("%.4f", float64(k)/float64(cells)))
	}
}
