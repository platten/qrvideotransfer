package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/spf13/cobra"
)

func (a *app) calibrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "Find the fastest settings your screen and camera can manage",
		Long: "calibrate make writes a test video that steps through modes, densities and display rates. " +
			"Play it full-screen on the screen you will use, film it with the phone you will use, " +
			"then calibrate read measures each step in the recording and recommends an encode command.",
		Example: "qrvt calibrate make -o cal.mp4\nqrvt calibrate read IMG_0420.MOV",
		Args:    cobra.ArbitraryArgs,
		RunE:    helpOrUnknown,
	}
	var mk struct {
		output, resolution string
		engine
	}
	mkCmd := &cobra.Command{
		Use:     "make -o cal.mp4",
		Short:   "Write the calibration video",
		Long:    "Write a test video that steps through modes, densities and display rates, for the monitor resolution given.",
		Example: "qrvt calibrate make -o cal.mp4 --resolution 2560x1440",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mk.output == "" {
				return usagef("--output is required")
			}
			w, h, err := parseResolution(mk.resolution)
			if err != nil {
				return usagef("--resolution %q: %v", mk.resolution, err)
			}
			if _, _, err := fitCell(w, h, 4, "use a smaller --resolution"); err != nil { // the smallest cell the video steps through
				return err
			}
			if err := mk.check(); err != nil {
				return err
			}
			o := qrvt.CalibrationOptions{Output: mk.output, Width: w, Height: h, Backend: mk.backend, Workers: mk.jobs}
			info := ui.Info{Command: "calibrate make", Output: o.Output, Video: []ui.Field{{Key: "Size", Value: fmt.Sprintf("%dx%d", w, h)}},
				Stages: []qrvt.Stage{qrvt.StageRender, qrvt.StageEncodeVideo}}
			return a.run(cmd, info, nil, func(ctx context.Context, _ ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
				o.Progress = progress
				if err := a.env.API.CalibrationVideo(ctx, o); err != nil {
					return nil, err
				}
				return &ui.Summary{Title: "Wrote " + o.Output, Fields: []ui.Field{
					{Key: "Size", Value: fmt.Sprintf("%dx%d", w, h)},
					{Key: "Next", Value: "play it full-screen, film it, then run qrvt calibrate read <recording>"},
				}}, nil
			})
		},
	}
	fs := mkCmd.Flags()
	fs.StringVarP(&mk.output, "output", "o", "", "The calibration video to write (required).\nExample: -o cal.mp4")
	fs.StringVar(&mk.resolution, "resolution", "1080p", "Resolution of the monitor that will play the video: a preset "+
		"(480p, 720p, 1080p, 1440p, 4k or its other name 2160p) or WxH.\nExample: --resolution 2560x1440")
	addEngineFlags(fs, &mk.engine, false)

	var rd engine
	read := &cobra.Command{
		Use:     "read <recording>...",
		Short:   "Measure a recording of the calibration video and recommend settings",
		Long:    "Measure each step of the calibration video in one or more recordings and print the recommended encode command, with warnings for clipping, dropped frames, moire and exposure.",
		Example: "qrvt calibrate read IMG_0420.MOV",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usagef("calibrate read needs at least one recording")
			}
			if err := checkRecordings(args); err != nil {
				return err
			}
			if err := rd.check(); err != nil {
				return err
			}
			o := qrvt.AnalyzeOptions{Recordings: args, Backend: rd.backend, Workers: rd.jobs, UnsandboxedHEVC: rd.hevc}
			info := ui.Info{Command: "calibrate read", Sources: args, Signal: true, Stages: []qrvt.Stage{qrvt.StageDecodeVideo, qrvt.StageDemodulate}}
			return a.run(cmd, info, nil, func(ctx context.Context, _ ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
				o.Progress = progress
				rec, err := a.env.API.AnalyzeCalibration(ctx, o)
				if err != nil {
					return nil, err
				}
				p := rec.Profile
				return &ui.Summary{Title: "Recommended settings", Warnings: rec.Warnings, Fields: []ui.Field{
					{Key: "Command", Value: rec.Command},
					{Key: "Video", Value: fmt.Sprintf("%dx%d at %d fps, %s, cell %d", p.Width, p.Height, p.FPS, modeName(p.Modulation), p.Cell)},
					{Key: "Protection", Value: fmt.Sprintf("%s, parity %d%%", protectionName(p.Protection), p.ParityPercent)},
					{Key: "Planning rate", Value: ui.FormatBytes(int64(rec.BytesPerSec)) + "/s"},
				}}, nil
			})
		},
	}
	addEngineFlags(read.Flags(), &rd, true)
	cmd.AddCommand(mkCmd, read)
	return cmd
}

func (a *app) inspectCmd() *cobra.Command {
	var e engine
	cmd := &cobra.Command{
		Use:     "inspect <video|recording>",
		Short:   "Show what a code video or a recording of one carries",
		Long:    "Read the headers and the descriptor of a code video, or of a recording of one, and print its settings and size without unpacking anything.",
		Example: "qrvt inspect transfer.mp4\nqrvt inspect IMG_0412.MOV",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usagef("inspect needs one video or recording")
			}
			if err := checkRecordings(args); err != nil {
				return err
			}
			if err := e.check(); err != nil {
				return err
			}
			o := qrvt.InspectOptions{Path: args[0], Backend: e.backend, Workers: e.jobs, UnsandboxedHEVC: e.hevc}
			info := ui.Info{Command: "inspect", Sources: args, Signal: true, Stages: []qrvt.Stage{qrvt.StageDecodeVideo, qrvt.StageDemodulate}}
			return a.run(cmd, info, nil, func(ctx context.Context, _ ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
				o.Progress = progress
				si, err := a.env.API.Inspect(ctx, o)
				if err != nil {
					return nil, err
				}
				return inspectSummary(o.Path, si), nil
			})
		},
	}
	addEngineFlags(cmd.Flags(), &e, true)
	return cmd
}

// inspectSummary describes what a code video carries.
func inspectSummary(path string, si *qrvt.StreamInfo) *ui.Summary {
	s := &ui.Summary{Title: "Code video " + path}
	add := func(k, v string) { s.Fields = append(s.Fields, ui.Field{Key: k, Value: v}) }
	add("Session", fmt.Sprintf("%016x", si.Session))
	if si.Width > 0 {
		add("Video", fmt.Sprintf("%dx%d", si.Width, si.Height))
	}
	add("Grid", fmt.Sprintf("%dx%d cells", si.GridX, si.GridY))
	add("Mode", modeName(si.Modulation))
	add("Protection", protectionName(si.Protection))
	frames := fmt.Sprint(si.Frames)
	if si.FPS > 0 {
		frames += fmt.Sprintf(" at %d fps, a loop of %s", si.FPS, ui.FormatDuration(time.Duration(si.Frames)*time.Second/time.Duration(si.FPS)))
	}
	add("Frames", frames)
	add("Stream", fmt.Sprintf("%s, hash %s", ui.FormatBytes(si.StreamLen), hex.EncodeToString(si.StreamHash[:])))
	add("Shards", fmt.Sprintf("%d bytes", si.ShardSize))
	add("Blocks", fmt.Sprintf("%d x (%d + %d)", si.Blocks, si.K, si.M))
	return s
}

func (a *app) keygenCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "keygen -o name",
		Short: "Make a key pair for signing transfers",
		Long: "Write an Ed25519 key pair: name.key, the private key, readable by you only, and name.pub, the public key. " +
			"Sign with encode --sign-key name.key; receivers check with decode --verify-key name.pub.",
		Example: "qrvt keygen -o alice",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return usagef("--output is required")
			}
			return a.run(cmd, ui.Info{Command: "keygen", Output: name}, nil, func(context.Context, ui.Renderer, func(qrvt.Event)) (*ui.Summary, error) {
				return a.keygen(name)
			})
		},
	}
	cmd.Flags().StringVarP(&name, "output", "o", "", "Name of the key files to write, without extension: name.key and name.pub (required).\nExample: -o alice")
	return cmd
}

// keygen writes name.key (owner-only) and name.pub, refusing to replace either.
func (a *app) keygen(name string) (*ui.Summary, error) {
	priv, pub := name+".key", name+".pub"
	for _, p := range []string{priv, pub} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("%s already exists; choose another name or remove it", p)
		}
	}
	public, private, err := a.env.API.GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := writeNew(priv, private, 0o600); err != nil {
		return nil, err
	}
	if err := writeNew(pub, public, 0o644); err != nil {
		_ = os.Remove(priv)
		return nil, err
	}
	fp := "unknown: the public key is not in the expected format"
	if k, ok := keyBytes(string(public), publicPrefix); ok {
		fp = fingerprint(k)
	}
	return &ui.Summary{Title: "Wrote a key pair", Fields: []ui.Field{
		{Key: "Private key", Value: priv + " (keep it secret)"},
		{Key: "Public key", Value: pub + " (give it to receivers)"},
		{Key: "Fingerprint", Value: fp},
	}}, nil
}

// writeNew creates a file that must not exist yet and writes b to it, ending with a line break.
func writeNew(path string, b []byte, perm fs.FileMode) error {
	if !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) //nolint:gosec // the user names the file
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	return errors.Join(err, f.Close())
}

func (a *app) profilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List the video presets, modes and protection levels",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(a.env.Out, profilesText())
			return err
		},
	}
}

// profilesText lists the presets with what a frame carries at each protection level, computed
// from the format (DESIGN.md section 11).
func profilesText() string {
	var b strings.Builder
	b.WriteString("Presets (--resolution), with cell 4, four greys and parity 20%: payload per frame\n")
	fmt.Fprintf(&b, "  %-6s %-10s %-8s %-11s %-10s %-10s %s\n", "preset", "size", "grid", "data cells", "fast", "normal", "robust")
	for _, p := range presets {
		gx, gy := p.w/4, p.h/4
		l, _ := frame.NewLayout(gx, gy) // the presets are valid grids
		fmt.Fprintf(&b, "  %-6s %-10s %-8s %-11d", p.name, fmt.Sprintf("%dx%d", p.w, p.h), fmt.Sprintf("%dx%d", gx, gy), len(l.DataCells()))
		for i, pr := range []qrvt.Protection{qrvt.Fast, qrvt.Normal, qrvt.Robust} {
			format := " %-10s"
			if i == 2 {
				format = " %s"
			}
			fmt.Fprintf(&b, format, ui.FormatBytes(framePayload(gx, gy, pr)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nModes (--mode): gray4 (default; four greys, 2 bits per cell), bw (black and white, 1 bit, half of gray4), " +
		"color (eight colours, 3 bits, one and a half times gray4); --hdr: eight greys in a 10-bit video (experimental)\n")
	b.WriteString("Protection (--protection): fast (LDPC rate 5/6, 192-byte shards), normal (default; 3/4, 128-byte shards), " +
		"robust (1/2, 64-byte shards)\n")
	b.WriteString("Frame rates (--fps): 12 (default), 30 or 60, or any rate from 1 to 240\n")
	b.WriteString("Cells (--cell): 4 (default), 8, 12 or 16 pixels\n")
	return b.String()
}

// framePayload is the net payload of a frame with four greys and parity 20%:
// (C - 1) x shard size x K / (K + M), with K / (K + M) = 100 / 120.
func framePayload(gx, gy int, p qrvt.Protection) int64 {
	h := frame.Header{Version: frame.Version, TotalFrames: 4, Modulation: uint8(qrvt.Gray4), InnerCode: uint8(p)}
	h.GridX, h.GridY = uint16(gx), uint16(gy) //nolint:gosec // preset grids are below 2048
	m, err := frame.NewMapping(h)
	if err != nil {
		return 0
	}
	in, _ := frame.LookupInner(uint8(p))
	return int64(m.CodewordsPerFrame()-1) * int64(in.ShardSize) * 100 / 120
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(a.env.Out, "qrvt %s (%s, %s/%s)\n", a.env.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}

func (a *app) manCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "man",
		Short: "Print the manual page",
		Long:  "Print the manual page in roff format, generated from the same definitions as --help.",
		Example: "qrvt man > qrvt.1\n" +
			"qrvt man | man -l -",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			page, err := manPage(cmd.Root())
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(a.env.Out, page)
			return err
		},
	}
}

func (a *app) demoCmd() *cobra.Command {
	var delay time.Duration
	cmd := &cobra.Command{
		Use:    "demo",
		Short:  "Play a recorded decode through the progress display",
		Long:   "Play a recorded decode through the dashboard, or through plain or JSON output with --plain or --json. Nothing is read or written.",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if delay < 0 {
				return usagef("--delay %v: use 0 or more", delay)
			}
			return a.run(cmd, ui.DemoInfo(), nil, func(ctx context.Context, _ ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
				for _, e := range ui.DemoEvents(time.Now()) {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(delay):
					}
					progress(e)
				}
				return ui.DemoSummary(), nil
			})
		},
	}
	cmd.Flags().DurationVar(&delay, "delay", 60*time.Millisecond, "Pause between events.\nExample: --delay 20ms")
	return cmd
}
