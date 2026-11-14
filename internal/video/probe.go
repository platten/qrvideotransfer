package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// probeInfo is everything qrvt takes from ffprobe, validated.
type probeInfo struct {
	ClipInfo
	PixFmt    string
	Start     float64 // stream start time, seconds
	Size      int64   // file size in bytes
	BitRate   int64   // bits per second; 0 if unknown
	Profile   string
	Range     string // "tv", "pc" or ""
	DolbyVis  bool   // a Dolby Vision configuration record is present
	Container string
}

// maxProbeOutput bounds what is read from ffprobe.
const maxProbeOutput = 1 << 20

func probeArgs(in string) []string {
	a := []string{"-v", "error", "-of", "json", "-select_streams", "v:0", "-show_entries",
		"stream=codec_name,profile,width,height,pix_fmt,r_frame_rate,avg_frame_rate,nb_frames,duration,start_time," +
			"color_transfer,color_range,bit_rate:stream_tags=rotate:stream_side_data_list:format=format_name,duration,bit_rate"}
	return append(append(a, inputOptions...), in)
}

// probe runs ffprobe on path through r and validates what it reports.
func probe(ctx context.Context, r runner, path string) (probeInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return probeInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return probeInfo{}, fmt.Errorf("%w: not a regular file", ErrInvalidVideo)
	}
	if st.Size() == 0 {
		return probeInfo{}, fmt.Errorf("%w: empty file", ErrInvalidVideo)
	}
	out := &capped{max: maxProbeOutput}
	err = run(ctx, r, job{prog: "ffprobe", args: func(in, _ string) []string { return probeArgs(in) },
		input: path, stdout: out, progress: progReads, check: probeLimits(st.Size(), EngineProbeTime)})
	if errors.Is(err, ErrEngine) {
		return probeInfo{}, fmt.Errorf("%w: %w", ErrInvalidVideo, err)
	}
	if err != nil {
		return probeInfo{}, err
	}
	if out.over {
		return probeInfo{}, fmt.Errorf("%w: probe output over %d bytes", ErrInvalidVideo, maxProbeOutput)
	}
	p, err := parseProbe(out.b.Bytes())
	p.Size = st.Size()
	return p, err
}

// capped is a buffer that keeps at most max bytes and notes whether more came.
type capped struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > c.max {
		c.over = true
		return len(p), nil
	}
	return c.b.Write(p)
}

type probeJSON struct {
	Streams []struct {
		CodecName     string `json:"codec_name"`
		Profile       string `json:"profile"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		PixFmt        string `json:"pix_fmt"`
		RFrameRate    string `json:"r_frame_rate"`
		AvgFrameRate  string `json:"avg_frame_rate"`
		NbFrames      string `json:"nb_frames"`
		Duration      string `json:"duration"`
		StartTime     string `json:"start_time"`
		ColorTransfer string `json:"color_transfer"`
		ColorRange    string `json:"color_range"`
		BitRate       string `json:"bit_rate"`
		Tags          struct {
			Rotate string `json:"rotate"`
		} `json:"tags"`
		SideData []struct {
			Type     string          `json:"side_data_type"`
			Rotation json.RawMessage `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// parseProbe validates ffprobe's JSON. Every number is checked before it is used.
func parseProbe(b []byte) (probeInfo, error) {
	var j probeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return probeInfo{}, fmt.Errorf("%w: unreadable probe output", ErrInvalidVideo)
	}
	if len(j.Streams) == 0 {
		return probeInfo{}, fmt.Errorf("%w: no video stream", ErrInvalidVideo)
	}
	s := j.Streams[0]
	p := probeInfo{
		ClipInfo: ClipInfo{Codec: sanitise(s.CodecName, 32), Width: s.Width, Height: s.Height,
			Transfer: sanitise(s.ColorTransfer, 32)},
		PixFmt: sanitise(s.PixFmt, 32), Profile: sanitise(s.Profile, 32), Range: sanitise(s.ColorRange, 8),
		Container: sanitise(j.Format.FormatName, 64),
	}
	if err := checkSize(p.Width, p.Height); err != nil {
		return probeInfo{}, err
	}
	if p.Width%2 != 0 || p.Height%2 != 0 {
		return probeInfo{}, fmt.Errorf("%w: odd picture size %dx%d", ErrUnsupported, p.Width, p.Height)
	}
	p.BitDepth = pixFmtDepth(p.PixFmt)
	p.FPSNum, p.FPSDen = rate(s.AvgFrameRate)
	if p.FPSNum == 0 {
		p.FPSNum, p.FPSDen = rate(s.RFrameRate)
	}
	p.Start = seconds(s.StartTime)
	dur := seconds(s.Duration)
	if dur == 0 {
		dur = seconds(j.Format.Duration)
	}
	p.Duration = time.Duration(dur * float64(time.Second))
	if n, err := strconv.ParseInt(s.NbFrames, 10, 64); err == nil && n > 0 && n < 1<<40 {
		p.Frames = n
	} else if p.FPSNum > 0 && dur > 0 {
		p.Frames = int64(math.Round(dur * float64(p.FPSNum) / float64(p.FPSDen)))
	}
	if p.BitRate = posInt(s.BitRate); p.BitRate == 0 {
		p.BitRate = posInt(j.Format.BitRate)
	}
	rot := posInt(s.Tags.Rotate)
	for _, sd := range s.SideData {
		if r, err := strconv.ParseFloat(string(sd.Rotation), 64); err == nil && !math.IsNaN(r) && math.Abs(r) <= 360 {
			rot = int64(math.Round(r))
		}
		if strings.Contains(strings.ToLower(sd.Type), "dovi") || strings.Contains(strings.ToLower(sd.Type), "dolby") {
			p.DolbyVis = true
		}
	}
	p.Rotation = int(((rot % 360) + 360) % 360)
	return p, nil
}

// pixFmtDepth is the bit depth a pixel format name implies.
func pixFmtDepth(f string) int {
	for _, d := range []string{"16", "14", "12", "10", "9"} {
		if strings.Contains(f, "p"+d) || strings.HasPrefix(f, "gray"+d) || strings.Contains(f, "p0"+d) {
			n, _ := strconv.Atoi(d)
			return n
		}
	}
	return 8
}

// rate parses a frame rate "num/den" within sane bounds (up to 1000 frames per second).
func rate(s string) (int, int) {
	a, b, ok := strings.Cut(s, "/")
	n, err1 := strconv.Atoi(a)
	d, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || n <= 0 || d <= 0 || d > 1<<24 || n > 1000*d {
		return 0, 0
	}
	return n, d
}

// seconds parses a non-negative time in seconds below about 31 years; anything else is 0.
func seconds(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > 1e9 {
		return 0
	}
	return v
}

func posInt(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
