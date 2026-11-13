// Command s1 is the throwaway M0 spike S1: it measures the embedded video
// engine (go-ffmpreg run by wazero). See docs/spikes/S1.md.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s1 info|gen|decode|encode|bitrate|stream|startup|ff [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	ucd, _ := os.UserCacheDir()
	cache := fs.String("cache", filepath.Join(ucd, "qrvt-spike-s1", "wazero"), "compilation cache dir (empty: none)")
	interp := fs.Bool("interp", false, "use the wazero interpreter")
	native := fs.Bool("native", false, "use the system ffmpeg instead of the engine")
	pages := fs.Uint("pages", 0, "wasm memory limit in 64 KiB pages, reserved up front (0: wazero default growth)")
	var err error
	ctx := context.Background()
	newEng := func() *engine {
		if *native {
			return &engine{native: true}
		}
		e, err := newEngine(ctx, *cache, *interp, uint32(*pages))
		if err != nil {
			fatal(err)
		}
		return e
	}
	switch cmd {
	case "info":
		fs.Parse(args)
		err = cmdInfo(ctx, newEng())
	case "gen":
		w := fs.Int("w", 1920, "width")
		h := fs.Int("h", 1080, "height")
		cell := fs.Int("cell", 5, "cell size in pixels")
		n := fs.Int("frames", 300, "frames")
		hold := fs.Int("hold", 1, "repeat each pattern for this many frames")
		fs.Parse(args)
		err = cmdGen(*w, *h, *cell, *n, *hold)
	case "decode":
		in := fs.String("in", "", "input file")
		luma := fs.Bool("luma", false, "brightness only (-vf extractplanes=y)")
		skip := fs.Bool("skiplf", false, "-skip_loop_filter all")
		n := fs.Int("frames", 0, "decode at most this many frames (0: all)")
		par := fs.Int("n", 1, "parallel instances")
		fs.IntVar(threadsFlag, "threads", 1, "decoder threads (matters for -native only; the engine is single-threaded)")
		fs.Parse(args)
		err = cmdDecode(ctx, newEng(), *in, *luma, *skip, *n, *par)
	case "encode":
		w := fs.Int("w", 1920, "width")
		h := fs.Int("h", 1080, "height")
		cell := fs.Int("cell", 4, "cell pitch")
		mod := fs.String("mod", "gray4", "gray2|gray4|rgb8|gray8 (gray8 is 10-bit)")
		n := fs.Int("frames", 30, "frames")
		codec := fs.String("codec", "x264", "x264|x264-10|aom10")
		qp := fs.Int("qp", 32, "quantiser (x264) or crf (aom)")
		preset := fs.String("preset", "medium", "x264 preset or aom cpu-used")
		extra := fs.String("extra", "", "extra encoder args, space separated")
		fs.StringVar(&x264Extra, "x264", "", "extra x264-params, appended (e.g. :no-deblock=1)")
		out := fs.String("out", "out/enc.mp4", "output file")
		par := fs.Int("n", 1, "parallel instances")
		check := fs.Bool("check", false, "decode the output and count cell errors")
		fs.Parse(args)
		err = cmdEncode(ctx, newEng(), encOpts{*w, *h, *cell, *mod, *n, *codec, *qp, *preset, *extra, *out, *par, *check})
	case "stream":
		w := fs.Int("w", 1920, "width")
		h := fs.Int("h", 1080, "height")
		gb := fs.Float64("gb", 10, "gigabytes to push through")
		slow := fs.Bool("slow", false, "consumer pauses periodically, to exercise backpressure")
		fs.Parse(args)
		err = cmdStream(ctx, newEng(), *w, *h, *gb, *slow)
	case "startup":
		fs.Parse(args)
		t0 := time.Now()
		e := newEng()
		t1 := time.Now()
		var out bytes.Buffer
		_, err = e.Run(ctx, run{Name: "ffmpeg", Args: []string{"-hide_banner", "-version"}, Stdout: &out})
		fmt.Printf("startup cache=%q compile+instantiate-runtime=%.2fs first-run(-version)=%.2fs total-in-main=%.2fs\n",
			*cache, t1.Sub(t0).Seconds(), time.Since(t1).Seconds(), time.Since(t0).Seconds())
	case "ff":
		dir := fs.String("dir", ".", "host directory mounted at /d")
		name := fs.String("name", "ffmpeg", "ffmpeg or ffprobe")
		fs.Parse(args)
		d, _ := filepath.Abs(*dir)
		var res result
		res, err = newEng().Run(ctx, run{Name: *name, Args: fs.Args(), Dir: d, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
		_, peak := rss()
		fmt.Fprintf(os.Stderr, "rc=%d wall=%.2fs wasm-peak=%.0fMB process-peak-rss=%.0fMB\n", res.RC, res.Wall.Seconds(), mb(res.PeakMem), mb(peak))
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fatal(err)
	}
}

var x264Extra string // appended to -x264-params; set by the encode command

var threadsFlag = new(int) // decoder threads; set by the decode command

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "s1:", err)
	os.Exit(1)
}

func cmdInfo(ctx context.Context, e *engine) error {
	fmt.Printf("goos=%s goarch=%s numcpu=%d go=%s compile=%.2fs\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), e.Compile.Seconds())
	for _, a := range [][]string{{"-hide_banner", "-version"}, {"-hide_banner", "-decoders"}, {"-hide_banner", "-encoders"}} {
		var out bytes.Buffer
		if _, err := e.Run(ctx, run{Name: "ffmpeg", Args: a, Stdout: &out}); err != nil {
			return err
		}
		for _, l := range strings.Split(out.String(), "\n") {
			if a[1] == "-version" || strings.Contains(l, "264") || strings.Contains(l, "hevc") || strings.Contains(l, "av1") || strings.Contains(l, "aom") || strings.Contains(l, "vp9") {
				fmt.Println(l)
			}
		}
	}
	return nil
}

// cmdGen writes w x h yuv420p frames of random four-level grey cells to stdout.
func cmdGen(w, h, cell, n, hold int) error {
	bw := bufio.NewWriterSize(os.Stdout, 1<<20)
	p := newPainter(w, h, cell, "gray4", 1)
	buf := make([]byte, p.frameSize())
	for i := 0; i < n; i++ {
		p.paint(buf, p.symbols(uint64(i/hold)))
		if _, err := bw.Write(buf); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// counter counts bytes written to it.
type counter struct {
	mu sync.Mutex
	n  int64
}

func (c *counter) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.n += int64(len(p))
	c.mu.Unlock()
	return len(p), nil
}

func frameBytes(pix string, w, h int) int {
	switch pix {
	case "gray":
		return w * h
	case "gray10le":
		return 2 * w * h
	case "yuv420p":
		return w*h + 2*(w/2)*(h/2)
	case "yuv420p10le":
		return 2 * (w*h + 2*(w/2)*(h/2))
	}
	return 0
}

func cmdDecode(ctx context.Context, e *engine, in string, luma, skip bool, maxFrames, par int) error {
	dir, name := splitPath(in)
	pi, err := e.probe(ctx, dir, name)
	if err != nil {
		return err
	}
	tenBit := strings.Contains(pi.PixFmt, "10")
	outPix := pi.PixFmt
	if luma {
		outPix = map[bool]string{false: "gray", true: "gray10le"}[tenBit]
	}
	fsz := frameBytes(outPix, pi.W, pi.H)
	if fsz == 0 {
		return fmt.Errorf("unsupported pix_fmt %s", outPix)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-noautorotate", "-threads", strconv.Itoa(*threadsFlag)}
	if skip {
		args = append(args, "-skip_loop_filter", "all")
	}
	args = append(args, "-i", "/d/"+name)
	if maxFrames > 0 {
		args = append(args, "-frames:v", strconv.Itoa(maxFrames))
	}
	if luma {
		args = append(args, "-vf", "extractplanes=y")
	}
	args = append(args, "-pix_fmt", outPix, "-f", "rawvideo", "pipe:1")
	fmt.Printf("decode %s: %s %dx%d %s trc=%s prim=%s space=%s range=%s packets=%d; out=%s luma=%v skiplf=%v n=%d native=%v\n",
		name, pi.Codec, pi.W, pi.H, pi.PixFmt, pi.Transfer, pi.Primaries, pi.Space, pi.Range, pi.Frames, outPix, luma, skip, par, e.native)
	done := make(chan struct{})
	go sampleRSS(done, nil)
	var wg sync.WaitGroup
	res := make([]result, par)
	cnt := make([]counter, par)
	errs := make([]error, par)
	errb := make([]bytes.Buffer, par)
	t0 := time.Now()
	for i := 0; i < par; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i], errs[i] = e.Run(ctx, run{Name: "ffmpeg", Args: args, Dir: dir, RO: true, Stdout: &cnt[i], Stderr: &errb[i]})
		}(i)
	}
	wg.Wait()
	wall := time.Since(t0)
	close(done)
	total := 0.0
	var peakMem uint64
	for i := 0; i < par; i++ {
		if errs[i] != nil || res[i].RC != 0 {
			return fmt.Errorf("instance %d rc=%d err=%v: %s", i, res[i].RC, errs[i], errb[i].String())
		}
		fr := float64(cnt[i].n) / float64(fsz)
		total += fr
		peakMem = max(peakMem, res[i].PeakMem)
		fmt.Printf("  instance %d: frames=%.0f wall=%.2fs fps=%.2f wasm-peak=%.0fMB\n", i, fr, res[i].Wall.Seconds(), fr/res[i].Wall.Seconds(), mb(res[i].PeakMem))
	}
	_, hwm := rss()
	fmt.Printf("RESULT decode %s luma=%v skiplf=%v n=%d native=%v: aggregate fps=%.2f per-instance fps=%.2f wall=%.2fs wasm-peak=%.0fMB process-peak-rss=%.0fMB\n",
		name, luma, skip, par, e.native, total/wall.Seconds(), total/wall.Seconds()/float64(par), wall.Seconds(), mb(peakMem), mb(hwm))
	return nil
}

// sampleRSS prints the process RSS every 5 s until done is closed, and
// appends samples to *out when out is non-nil.
func sampleRSS(done chan struct{}, out *[]uint64) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			cur, _ := rss()
			if out != nil {
				*out = append(*out, cur)
			}
		}
	}
}

// painter renders frames of cells in the default appearance of FORMAT.md section 7.
type painter struct {
	w, h, cell, gx, gy int
	mod                string
	seed               uint64
}

func newPainter(w, h, cell int, mod string, seed uint64) *painter {
	return &painter{w: w, h: h, cell: cell, gx: w / cell, gy: h / cell, mod: mod, seed: seed}
}

var (
	gray2 = []int{16, 235}
	gray4 = []int{16, 89, 162, 235}
	gray8 = []int{64, 189, 314, 439, 565, 690, 815, 940} // 10-bit
	rgb8  = [][3]int{{16, 128, 128}, {32, 240, 118}, {173, 42, 26}, {188, 154, 16}, {63, 102, 240}, {78, 214, 230}, {219, 16, 138}, {235, 128, 128}}
)

func (p *painter) levels() int {
	return map[string]int{"gray2": 2, "gray4": 4, "rgb8": 8, "gray8": 8}[p.mod]
}

func (p *painter) bits() float64 {
	return map[string]float64{"gray2": 1, "gray4": 2, "rgb8": 3, "gray8": 3}[p.mod]
}

func (p *painter) tenBit() bool { return p.mod == "gray8" }

func (p *painter) frameSize() int {
	if p.tenBit() {
		return frameBytes("yuv420p10le", p.w, p.h)
	}
	return frameBytes("yuv420p", p.w, p.h)
}

// symbols returns the deterministic symbols of frame f.
func (p *painter) symbols(f uint64) []byte {
	r := rand.New(rand.NewPCG(p.seed, f))
	s := make([]byte, p.gx*p.gy)
	L := uint64(p.levels())
	for i := range s {
		s[i] = byte(r.Uint64() % L)
	}
	return s
}

// paint renders symbols into a planar 4:2:0 frame (8- or 10-bit LE).
func (p *painter) paint(buf []byte, sym []byte) {
	w, h, c := p.w, p.h, p.cell
	cw, ch := w/2, h/2
	ySz := w * h
	if p.tenBit() {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := gray8[sym[(y/c)*p.gx+x/c]]
				binary.LittleEndian.PutUint16(buf[2*(y*w+x):], uint16(v))
			}
		}
		for i := 2 * ySz; i < len(buf); i += 2 {
			binary.LittleEndian.PutUint16(buf[i:], 512)
		}
		return
	}
	for y := 0; y < h; y++ {
		row := buf[y*w : (y+1)*w]
		srow := sym[(y/c)*p.gx:]
		for x := range row {
			s := srow[x/c]
			switch p.mod {
			case "gray2":
				row[x] = byte(gray2[s])
			case "gray4":
				row[x] = byte(gray4[s])
			default:
				row[x] = byte(rgb8[s][0])
			}
		}
	}
	u, v := buf[ySz:ySz+cw*ch], buf[ySz+cw*ch:]
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			cu, cv := 128, 128
			if p.mod == "rgb8" {
				s := sym[(2*y/c)*p.gx+2*x/c]
				cu, cv = rgb8[s][1], rgb8[s][2]
			}
			u[y*cw+x], v[y*cw+x] = byte(cu), byte(cv)
		}
	}
}

// classify compares a decoded 8-bit yuv420p (or gray, when luma) frame,
// or a 10-bit one for gray8, with the symbols and returns the cell errors.
func (p *painter) classify(frame []byte, sym []byte, luma bool) int {
	w, c := p.w, p.cell
	lo, hi := c/4, c-c/4 // central region of the cell
	errs := 0
	for gy := 0; gy < p.gy; gy++ {
		for gx := 0; gx < p.gx; gx++ {
			var sy, n int
			for y := gy*c + lo; y < gy*c+hi; y++ {
				for x := gx*c + lo; x < gx*c+hi; x++ {
					if p.tenBit() {
						sy += int(binary.LittleEndian.Uint16(frame[2*(y*w+x):]))
					} else {
						sy += int(frame[y*w+x])
					}
					n++
				}
			}
			Y := float64(sy) / float64(n)
			var best, bestD = 0, 1e18
			switch p.mod {
			case "rgb8":
				ySz, cw := p.w*p.h, p.w/2
				var su, sv, m int
				for y := (gy*c + lo) / 2; y < (gy*c+hi+1)/2; y++ {
					for x := (gx*c + lo) / 2; x < (gx*c+hi+1)/2; x++ {
						su += int(frame[ySz+y*cw+x])
						sv += int(frame[ySz+(p.h/2)*cw+y*cw+x])
						m++
					}
				}
				U, V := float64(su)/float64(m), float64(sv)/float64(m)
				for k, t := range rgb8 {
					d := sq(Y-float64(t[0])) + sq(U-float64(t[1])) + sq(V-float64(t[2]))
					if d < bestD {
						best, bestD = k, d
					}
				}
			default:
				lv := map[string][]int{"gray2": gray2, "gray4": gray4, "gray8": gray8}[p.mod]
				for k, t := range lv {
					if d := sq(Y - float64(t)); d < bestD {
						best, bestD = k, d
					}
				}
			}
			if best != int(sym[gy*p.gx+gx]) {
				errs++
			}
		}
	}
	return errs
}

func sq(x float64) float64 { return x * x }

type encOpts struct {
	w, h, cell         int
	mod                string
	frames             int
	codec              string
	qp                 int
	preset, extra, out string
	par                int
	check              bool
}

func cmdEncode(ctx context.Context, e *engine, o encOpts) error {
	p := newPainter(o.w, o.h, o.cell, o.mod, 7)
	if err := os.MkdirAll(filepath.Dir(o.out), 0o755); err != nil {
		return err
	}
	dir, name := splitPath(o.out)
	inPix := "yuv420p"
	if p.tenBit() {
		inPix = "yuv420p10le"
	}
	tags := []string{"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709"}
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", inPix, "-s", fmt.Sprintf("%dx%d", o.w, o.h), "-r", "30"}
	args = append(args, tags...)
	args = append(args, "-i", "pipe:0")
	switch o.codec {
	case "x264", "x264-10":
		prof := "high"
		if o.codec == "x264-10" {
			prof = "high10"
		}
		args = append(args, "-c:v", "libx264", "-preset", o.preset, "-profile:v", prof, "-qp", strconv.Itoa(o.qp),
			"-x264-params", "keyint=1:scenecut=0:bframes=0:cabac=1"+x264Extra)
	case "vp9-10":
		args = append(args, "-c:v", "libvpx-vp9", "-profile:v", "2", "-deadline", "realtime", "-cpu-used", o.preset,
			"-crf", strconv.Itoa(o.qp), "-b:v", "0", "-g", "1")
	case "aom10":
		args = append(args, "-c:v", "libaom-av1", "-cpu-used", o.preset, "-crf", strconv.Itoa(o.qp), "-b:v", "0", "-g", "1")
	default:
		return fmt.Errorf("codec %s", o.codec)
	}
	if o.extra != "" {
		args = append(args, strings.Fields(o.extra)...)
	}
	args = append(args, tags...)
	args = append(args, "-pix_fmt", inPix, "-y")
	fsz := p.frameSize()
	fmt.Printf("encode %dx%d cell=%d mod=%s frames=%d codec=%s qp=%d preset=%s extra=%q n=%d native=%v\n",
		o.w, o.h, o.cell, o.mod, o.frames, o.codec, o.qp, o.preset, o.extra, o.par, e.native)
	// Pre-render a handful of distinct frames so that Go's painting does not
	// dominate the measurement; frame i uses pattern i mod len(pre).
	pre := make([][]byte, min(o.frames, 8))
	syms := make([][]byte, len(pre))
	for i := range pre {
		syms[i] = p.symbols(uint64(i))
		pre[i] = make([]byte, fsz)
		p.paint(pre[i], syms[i])
	}
	var wg sync.WaitGroup
	res := make([]result, o.par)
	errs := make([]error, o.par)
	errb := make([]bytes.Buffer, o.par)
	t0 := time.Now()
	for k := 0; k < o.par; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			pr, pw := io.Pipe()
			go func() {
				for i := 0; i < o.frames; i++ {
					if _, err := pw.Write(pre[i%len(pre)]); err != nil {
						return
					}
				}
				pw.Close()
			}()
			outName := name
			if k > 0 {
				outName = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(name, filepath.Ext(name)), k, filepath.Ext(name))
			}
			a := append(append([]string{}, args...), "/d/"+outName)
			res[k], errs[k] = e.Run(ctx, run{Name: "ffmpeg", Args: a, Dir: dir, Stdin: pr, Stderr: &errb[k]})
			pr.CloseWithError(errors.New("encoder finished"))
		}(k)
	}
	wg.Wait()
	wall := time.Since(t0)
	var peakMem uint64
	for k := 0; k < o.par; k++ {
		if errs[k] != nil || res[k].RC != 0 {
			return fmt.Errorf("instance %d rc=%d err=%v: %s", k, res[k].RC, errs[k], errb[k].String())
		}
		peakMem = max(peakMem, res[k].PeakMem)
	}
	st, err := os.Stat(o.out)
	if err != nil {
		return err
	}
	cells := float64(p.gx * p.gy * o.frames)
	bpc := float64(st.Size()*8) / cells
	_, hwm := rss()
	fmt.Printf("RESULT encode %dx%d %s %s qp=%d preset=%s n=%d native=%v: per-instance fps=%.2f aggregate fps=%.2f size=%d bits/cell=%.2f (payload %.0f bits/cell) wasm-peak=%.0fMB process-peak-rss=%.0fMB\n",
		o.w, o.h, o.mod, o.codec, o.qp, o.preset, o.par, e.native, float64(o.frames)/res[0].Wall.Seconds(), float64(o.frames*o.par)/wall.Seconds(),
		st.Size(), bpc, p.bits(), mb(peakMem), mb(hwm))
	if !o.check {
		return nil
	}
	pi, err := e.probe(ctx, dir, name)
	if err != nil {
		return err
	}
	fmt.Printf("  probe: %s %dx%d %s trc=%s prim=%s space=%s range=%s packets=%d\n", pi.Codec, pi.W, pi.H, pi.PixFmt, pi.Transfer, pi.Primaries, pi.Space, pi.Range, pi.Frames)
	for _, luma := range []bool{false, true} {
		if luma && p.mod == "rgb8" {
			continue
		}
		outPix := inPix
		vf := []string{}
		if luma {
			outPix = map[bool]string{false: "gray", true: "gray10le"}[p.tenBit()]
			vf = []string{"-vf", "extractplanes=y"}
		}
		ofsz := frameBytes(outPix, o.w, o.h)
		a := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-noautorotate", "-i", "/d/" + name}
		a = append(append(a, vf...), "-pix_fmt", outPix, "-f", "rawvideo", "pipe:1")
		pr, pw := io.Pipe()
		var r result
		var rerr error
		var eb bytes.Buffer
		go func() {
			r, rerr = e.Run(ctx, run{Name: "ffmpeg", Args: a, Dir: dir, RO: true, Stdout: pw, Stderr: &eb})
			pw.Close()
		}()
		frame := make([]byte, ofsz)
		nf, cellErr := 0, 0
		for {
			if _, err := io.ReadFull(pr, frame); err != nil {
				break
			}
			cellErr += p.classify(frame, syms[nf%len(syms)], luma)
			nf++
		}
		if rerr != nil || r.RC != 0 {
			return fmt.Errorf("check decode rc=%d err=%v: %s", r.RC, rerr, eb.String())
		}
		fmt.Printf("RESULT check %s luma=%v: frames=%d cell errors=%d of %d\n", o.mod, luma, nf, cellErr, nf*p.gx*p.gy)
	}
	return nil
}

// cmdStream pushes gb gigabytes of raw yuv420p frames through the engine
// (stdin to stdout, rawvideo passthrough) and verifies every byte.
func cmdStream(ctx context.Context, e *engine, w, h int, gb float64, slow bool) error {
	fsz := frameBytes("yuv420p", w, h)
	n := int(gb * 1e9 / float64(fsz))
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", w, h), "-r", "30",
		"-i", "pipe:0", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1"}
	fill := func(buf []byte, i int) {
		x := uint64(i)*0x9E3779B97F4A7C15 + 1
		for j := 0; j+8 <= len(buf); j += 8 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			binary.LittleEndian.PutUint64(buf[j:], x)
		}
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		buf := make([]byte, fsz)
		for i := 0; i < n; i++ {
			fill(buf, i)
			if _, err := inW.Write(buf); err != nil {
				return
			}
		}
		inW.Close()
	}()
	var res result
	var rerr error
	var eb bytes.Buffer
	go func() {
		res, rerr = e.Run(ctx, run{Name: "ffmpeg", Args: args, Stdin: inR, Stdout: outW, Stderr: &eb})
		outW.Close()
	}()
	var samples []uint64
	done := make(chan struct{})
	go sampleRSS(done, &samples)
	t0 := time.Now()
	got, want := make([]byte, fsz), make([]byte, fsz)
	frames, bad := 0, 0
	for {
		if _, err := io.ReadFull(outR, got); err != nil {
			break
		}
		fill(want, frames)
		if !bytes.Equal(got, want) {
			bad++
		}
		frames++
		if slow && frames%100 == 0 {
			time.Sleep(500 * time.Millisecond)
		}
		if frames%500 == 0 {
			cur, _ := rss()
			fmt.Printf("  %d frames %.2f GB rss=%.0fMB %.0f MB/s\n", frames, float64(frames)*float64(fsz)/1e9, mb(cur), float64(frames)*float64(fsz)/1e6/time.Since(t0).Seconds())
		}
	}
	close(done)
	wall := time.Since(t0)
	if rerr != nil || res.RC != 0 {
		return fmt.Errorf("stream rc=%d err=%v: %s", res.RC, rerr, eb.String())
	}
	lo, hi := ^uint64(0), uint64(0)
	for _, s := range samples {
		lo, hi = min(lo, s), max(hi, s)
	}
	_, hwm := rss()
	fmt.Printf("RESULT stream %dx%d: frames=%d/%d bytes=%.2fGB mismatched=%d wall=%.1fs %.0f MB/s rss samples min=%.0fMB max=%.0fMB (n=%d) process-peak-rss=%.0fMB wasm-peak=%.0fMB slow=%v\n",
		w, h, frames, n, float64(frames)*float64(fsz)/1e9, bad, wall.Seconds(), float64(frames)*float64(fsz)/1e6/wall.Seconds(), mb(lo), mb(hi), len(samples), mb(hwm), mb(res.PeakMem), slow)
	return nil
}
