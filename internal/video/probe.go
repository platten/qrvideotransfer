package video

import (
	"bytes"
	"context"
	"encoding/json"
)

// probeInfo is everything qrvt takes from ffprobe, validated.
type probeInfo struct {
	ClipInfo
	PixFmt    string
	Start     float64
	Size      int64
	BitRate   int64
	Profile   string
	Range     string
	DolbyVis  bool
	Container string
}

// maxProbeOutput bounds what is read from ffprobe.
const maxProbeOutput = 1 << 20

func probeArgs(in string) []string { panic("not implemented") }

// probe runs ffprobe on path through r and validates what it reports.
func probe(ctx context.Context, r runner, path string) (probeInfo, error) { panic("not implemented") }

// capped is a buffer that keeps at most max bytes and notes whether more came.
type capped struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) { panic("not implemented") }

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
func parseProbe(b []byte) (probeInfo, error) { panic("not implemented") }

// pixFmtDepth is the bit depth a pixel format name implies.
func pixFmtDepth(f string) int { panic("not implemented") }

// rate parses a frame rate "num/den" within sane bounds (up to 1000 frames per second).
func rate(s string) (int, int) { panic("not implemented") }

// seconds parses a non-negative time in seconds below about 31 years; anything else is 0.
func seconds(s string) float64 { panic("not implemented") }

func posInt(s string) int64 { panic("not implemented") }
