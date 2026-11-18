//go:build realdevice

package pipeline

// The real-device decode check (DESIGN.md section 15, "Real recordings"): it runs on the owner's
// machine only, never in CI, with QRVT_RECORDINGS naming a folder of recordings of the S2 test
// pattern (spikes/pattern). Nothing from the recordings is written anywhere: the output names a
// recording only by its place in the folder's sorted listing.
//
// The pattern's data cells carry no codewords: every codeword bit is zero and each cell shows its
// scramble bits alone (FORMAT.md section 9), and codeword 0 is no stream descriptor. The public
// decode path therefore cannot succeed on it: no descriptor ever decodes, and Decode reports that
// nothing is known (QRVT_REALDEVICE_PUBLIC=1 runs it to show that). The check runs instead every
// stage of the decode path up to the inner code: the vision workers, the phase-to-frame
// assignment, descrambling, the soft-bit sums across captures with the adjacent-capture weight,
// and the inner code's decoder with its CRC. The expected codeword is all zeros, which is a valid
// LDPC codeword but fails the CRC, so each codeword's soft bits are moved onto the coset of the
// all-zero shard's codeword (its sign flipped wherever that codeword has a 1) before decoding: the
// layered min-sum decoder treats a soft bit and its negation symmetrically, so it converges to the
// all-zero shard's codeword exactly when it would converge to all zeros, and the CRC then checks
// it. A codeword counts as decoded when the decoder reports success and the shard is all zeros.
//
// The pattern's frames carry inner code 2 (`normal`). Because every position holds a zero
// codeword bit, the same soft bits are also gathered with the interleaver of inner code 4
// (`robust`) and decoded with that code, which is what a `robust` transfer shown at the same
// setting would have given.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// rdSegment is one segment of a pattern file's table (spikes/pattern/README.md).
type rdSegment struct {
	Rate       int    `json:"rate"`
	Cell       int    `json:"cell"`
	Modulation uint8  `json:"modulation"`
	FirstIndex uint32 `json:"first_index"`
	CodeFrames int    `json:"code_frames"`
	GridX      int    `json:"grid_x"`
	GridY      int    `json:"grid_y"`
}

type rdPlan struct {
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	TotalFrames uint32      `json:"total_frames"`
	Segments    []rdSegment `json:"segments"`
}

// rdLocate returns the plan and segment of a header's parameters and frame index.
func rdLocate(plans []rdPlan, key params, idx uint32) (int, int, bool) {
	if key.session != 0x5332 || key.inner != 2 {
		return 0, 0, false
	}
	for pi, pl := range plans {
		if pl.TotalFrames != key.frames {
			continue
		}
		for si, s := range pl.Segments {
			if idx >= s.FirstIndex && idx < s.FirstIndex+uint32(s.CodeFrames) && s.GridX == int(key.gridX) && s.GridY == int(key.gridY) && s.Modulation == key.mod {
				return pi, si, true
			}
		}
	}
	return 0, 0, false
}

// rdInners are the inner codes each frame is decoded with.
var rdInners = [2]uint8{2, 4}

// rdFrame is one code frame's summed soft bits, by inner code and codeword.
type rdFrame struct {
	plan, seg int
	geoms     [2]*geometry
	soft      [2][]int8 // C*sent per inner code
	last      int
	captures  int
}

// rdCount is what a segment's decoding gave.
type rdCount struct {
	captures, located   int
	frames, wholeFrames [2]int // frames finalised with soft bits; frames whose every codeword decoded
	tried, ok, wrong    [2]int // codewords with soft bits; decoded to zeros; decoded to something else
	mixedCaptures       int    // captures that credited two code frames
	lateAdds            int    // soft bits for a frame already finalised, not counted
}

// TestRealDeviceDecode decodes each recording in QRVT_RECORDINGS as described above and logs, per
// pattern segment, the codewords decoded with `normal` and `robust`.
func TestRealDeviceDecode(t *testing.T) {
	dir := os.Getenv("QRVT_RECORDINGS")
	if dir == "" {
		t.Skip("set QRVT_RECORDINGS to a folder of recordings of the S2 pattern")
	}
	patterns := os.Getenv("QRVT_PATTERNS")
	if patterns == "" {
		patterns = filepath.Join(dir, "..", "patterns")
	}
	names, _ := filepath.Glob(filepath.Join(patterns, "pattern-*.json"))
	var plans []rdPlan
	for _, n := range names {
		b, err := os.ReadFile(n) //nolint:gosec // the owner's folder
		if err != nil {
			t.Fatal(err)
		}
		var p rdPlan
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, p)
	}
	if len(plans) == 0 {
		t.Fatal("no pattern tables (pattern-*.json) beside the recordings; set QRVT_PATTERNS")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var recs []string
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mov", ".mp4", ".mkv", ".m4v", ".ts":
			recs = append(recs, e.Name())
		}
	}
	slices.Sort(recs)
	only := map[int]bool{}
	for _, s := range strings.Split(os.Getenv("QRVT_RECORDING_INDEX"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			only[n] = true
		}
	}
	be, err := video.NewSystem("system")
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range recs {
		label := fmt.Sprintf("recording %d", i+1)
		if strings.Contains(strings.ToLower(name), "margin") {
			label += " (margin)"
		}
		if len(only) > 0 && !only[i+1] {
			continue
		}
		path := filepath.Join(dir, name)
		rdRecording(t, be, path, label, plans)
		if os.Getenv("QRVT_REALDEVICE_PUBLIC") == "1" {
			t0 := time.Now()
			_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: be, TempDir: t.TempDir(),
				Unpack: payload.UnpackOptions{OutputDir: filepath.Join(t.TempDir(), "out")}})
			var short *ShortError
			t.Logf("%s, public Decode: %v (ShortError with no blocks: %v), %.0f s", label, err,
				errors.As(err, &short) && len(short.Blocks) == 0, time.Since(t0).Seconds())
		}
	}
}

// rdRecording runs one recording through the vision workers and the inner codes.
func rdRecording(t *testing.T, be video.Backend, path, label string, plans []rdPlan) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Now()
	info, err := be.Probe(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := be.NewDecoder(ctx, path, video.DecodeConfig{BitDepth: info.BitDepth})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	workers := runtime.GOMAXPROCS(0)

	// A worker's result: the capture's segment and, per code frame shown, soft bits per inner code.
	type shown struct {
		frame uint32
		soft  [2][]int8
		geoms [2]*geometry
	}
	type result struct {
		index, plan, seg int
		located, placed  bool
		frames           []shown
	}
	type job struct {
		f     *video.Frame
		index int
	}
	free := make(chan *video.Frame, workers+2)
	for range workers + 2 {
		free <- &video.Frame{}
	}
	jobs := make(chan job)
	results := make(chan result, workers)
	var readErr error
	go func() {
		defer close(jobs)
		for i := 0; ; i++ {
			f := <-free
			if err := dec.Next(f); err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				return
			}
			jobs <- job{f, i}
		}
	}()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var w captureWorker
			alt := map[params]*geometry{}
			for j := range jobs {
				r := result{index: j.index}
				var cr captureResult
				w.see(j.f, &cr, func(g *geometry, idx uint32, soft []int8) {
					pi, si, ok := rdLocate(plans, cr.key, idx)
					if !ok || (r.placed && (pi != r.plan || si != r.seg)) {
						return
					}
					r.placed, r.plan, r.seg = true, pi, si
					k4 := cr.key
					k4.inner = rdInners[1]
					g4 := alt[k4]
					if g4 == nil {
						var err error
						if g4, err = newGeometry(k4); err != nil {
							return
						}
						alt[k4] = g4
					}
					s := shown{frame: idx, geoms: [2]*geometry{g, g4}}
					for k, gg := range s.geoms {
						s.soft[k] = make([]int8, gg.cpf*gg.sent)
						gg.gather(s.soft[k], soft, idx)
					}
					r.frames = append(r.frames, s)
				})
				r.located = cr.located
				free <- j.f
				results <- r
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	// The LDPC workers decode finalised frames; each owns its codes.
	type done struct {
		plan, seg        int
		tried, ok, wrong [2]int
	}
	finals := make(chan *rdFrame, workers)
	dones := make(chan done, workers)
	var lwg sync.WaitGroup
	for range workers {
		lwg.Add(1)
		go func() {
			defer lwg.Done()
			var codes [2]*ldpc.Code
			var coset [2][]byte
			for k, id := range rdInners {
				codes[k], _ = ldpc.New(id)
				zero := make([]byte, codes[k].ShardSize())
				coset[k] = make([]byte, (codes[k].N()+7)/8)
				codes[k].Encode(coset[k], zero)
			}
			llr := make([]int8, 2048)
			shard := make([]byte, 256)
			for fr := range finals {
				d := done{plan: fr.plan, seg: fr.seg}
				for k, g := range fr.geoms {
					for c := range g.cpf {
						src := fr.soft[k][c*g.sent : (c+1)*g.sent]
						nz := false
						for t, v := range src {
							if coset[k][t/8]>>(7-t%8)&1 == 1 {
								v = neg(v)
							}
							llr[t] = v
							nz = nz || v != 0
						}
						if !nz {
							continue
						}
						d.tried[k]++
						sh := shard[:codes[k].ShardSize()]
						if _, ok := codes[k].Decode(sh, llr[:g.sent]); !ok {
							continue
						}
						if slices.ContainsFunc(sh, func(b byte) bool { return b != 0 }) {
							d.wrong[k]++
						} else {
							d.ok[k]++
						}
					}
				}
				dones <- d
			}
		}()
	}
	go func() { lwg.Wait(); close(dones) }()

	counts := map[[2]int]*rdCount{}
	seg := func(p, s int) *rdCount {
		c := counts[[2]int{p, s}]
		if c == nil {
			c = &rdCount{}
			counts[[2]int{p, s}] = c
		}
		return c
	}
	var mu sync.Mutex // guards counts between the collector and the loop below
	collected := make(chan struct{})
	go func() {
		for d := range dones {
			mu.Lock()
			c := seg(d.plan, d.seg)
			for k := range 2 {
				c.tried[k] += d.tried[k]
				c.ok[k] += d.ok[k]
				c.wrong[k] += d.wrong[k]
				if d.tried[k] > 0 {
					c.frames[k]++
				}
				if g := d.tried[k]; g > 0 && d.ok[k] == g {
					c.wholeFrames[k]++
				}
			}
			mu.Unlock()
		}
		close(collected)
	}()

	open := map[uint32]*rdFrame{}
	finished := map[[2]uint32]bool{} // (plan, frame) finalised
	finalise := func(idx uint32, fr *rdFrame) {
		delete(open, idx)
		finished[[2]uint32{uint32(fr.plan), idx}] = true //nolint:gosec // small
		finals <- fr
	}
	waiting := map[int]result{}
	next, captures, located := 0, 0, 0
	for r := range results {
		waiting[r.index] = r
		for {
			r, ok := waiting[next]
			if !ok {
				break
			}
			delete(waiting, next)
			next++
			captures++
			if r.located {
				located++
			}
			if r.placed {
				mu.Lock()
				c := seg(r.plan, r.seg)
				c.captures++
				if len(r.frames) > 1 {
					c.mixedCaptures++
				}
				mu.Unlock()
			}
			for _, s := range r.frames {
				if finished[[2]uint32{uint32(r.plan), s.frame}] { //nolint:gosec // small
					mu.Lock()
					seg(r.plan, r.seg).lateAdds++
					mu.Unlock()
					continue
				}
				fr := open[s.frame]
				if fr == nil {
					fr = &rdFrame{plan: r.plan, seg: r.seg, geoms: s.geoms, last: -2}
					for k, g := range s.geoms {
						fr.soft[k] = make([]int8, g.cpf*g.sent)
					}
					open[s.frame] = fr
				}
				w := 2
				if fr.last == r.index-1 {
					w = 1
				}
				for k := range 2 {
					addSoft(fr.soft[k], s.soft[k], w)
				}
				fr.last = r.index
				fr.captures++
			}
			for idx, fr := range open {
				if fr.last <= r.index-3 {
					finalise(idx, fr)
				}
			}
		}
	}
	for idx, fr := range open {
		finalise(idx, fr)
	}
	close(finals)
	<-collected
	if readErr != nil {
		t.Fatalf("%s: %v", label, readErr)
	}
	fps := float64(info.FPSNum) / float64(max(info.FPSDen, 1))
	t.Logf("%s: camera %dx%d, %d-bit %s, %.2f fps; %d captures, %d located; %.0f s", label, info.Width, info.Height,
		info.BitDepth, info.Transfer, fps, captures, located, time.Since(t0).Seconds())
	var keys [][2]int
	for k := range counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]int) int {
		if a[0] != b[0] {
			return a[0] - b[0]
		}
		return a[1] - b[1]
	})
	t.Logf("| Pattern | # | Rate | Cell | Mod | Captures placed | Mixed | Late | Frames (of shown) | normal: codewords decoded / tried (wrong) | normal: frames whole | robust: decoded / tried (wrong) | robust: frames whole |")
	t.Logf("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, k := range keys {
		c, pl := counts[k], plans[k[0]]
		s := pl.Segments[k[1]]
		t.Logf("| %dx%d | %d | %d | %d | %s | %d | %d | %d | %d (%d) | %d / %d (%d) | %d | %d / %d (%d) | %d |", pl.Width, pl.Height, k[1], s.Rate, s.Cell,
			map[uint8]string{1: "Gray2", 2: "Gray4", 3: "RGB8"}[s.Modulation], c.captures, c.mixedCaptures, c.lateAdds, c.frames[0], s.CodeFrames,
			c.ok[0], c.tried[0], c.wrong[0], c.wholeFrames[0], c.ok[1], c.tried[1], c.wrong[1], c.wholeFrames[1])
	}
}
