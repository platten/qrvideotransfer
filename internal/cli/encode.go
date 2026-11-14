package cli

import (
	"context"
	"fmt"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
	"github.com/spf13/cobra"
)

// encodeFlags are the flags of qrvt encode.
type encodeFlags struct {
	output, resolution, mode, protection, compress, passFile, signKey string
	fps, parity, cell                                                 int
	hdr, encrypt, dryRun                                              bool
	engine
}

func (a *app) encodeCmd() *cobra.Command {
	var f encodeFlags
	cmd := &cobra.Command{
		Use:   "encode <path>... -o out.mp4",
		Short: "Turn files and folders into a looping code video",
		Long: "Pack the files and folders given into one archive, compress, protect and optionally encrypt and sign it, " +
			"and write a looping video of codes. Play the video full-screen and film it with qrvt decode in mind.\n\n" +
			"The video size is reduced to a multiple of the cell size, and the size used is reported.",
		Example: "qrvt encode report.pdf -o report.mp4\n" +
			"qrvt encode photos/ -o photos.mp4 --resolution 1440p --fps 30 --protection robust\n" +
			"qrvt encode secrets/ -o s.mp4 --encrypt --sign-key me.key\n" +
			"qrvt encode big.iso -o big.mp4 --dry-run",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return a.encode(cmd, args, &f) },
	}
	fs := cmd.Flags()
	fs.StringVarP(&f.output, "output", "o", "", "The video to write (required).\nExample: -o transfer.mp4")
	fs.StringVar(&f.resolution, "resolution", "1080p", "Video size: a preset (480p, 720p, 1080p, 1440p, 4k or its other name 2160p) or WxH. "+
		"The height must be at least 480 and the grid at most 2047 cells each way.\nExample: --resolution 1440p or --resolution 2560x1600")
	fs.IntVar(&f.fps, "fps", 12, "Display frame rate, 1 to 240. Your camera should record at least twice this rate.\nExample: --fps 30")
	fs.StringVar(&f.mode, "mode", "gray4", "How cells carry bits: gray4 (four greys, 2 bits), bw (black and white, 1 bit, the most robust) "+
		"or color (eight colours, 3 bits).\nExample: --mode bw")
	fs.BoolVar(&f.hdr, "hdr", false, "Experimental: eight greys in a 10-bit video, 3 bits per cell. Replaces --mode.\nExample: --hdr")
	fs.StringVar(&f.protection, "protection", "normal", "Error correction inside each frame: fast (LDPC rate 5/6), normal (3/4) "+
		"or robust (1/2, for poor recordings).\nExample: --protection robust")
	fs.IntVar(&f.parity, "parity", 20, "Parity shards as a percentage of data shards, 5 to 100. "+
		"At 20, any 83% of the loop is enough.\nExample: --parity 40")
	fs.IntVar(&f.cell, "cell", 4, "Cell size in pixels: 4, 8, 12 or 16. Larger cells survive a poorer camera but carry less.\nExample: --cell 8")
	fs.StringVar(&f.compress, "compress", "auto", "Compression: auto (zstd unless a sample does not compress), zstd, xz or none.\nExample: --compress xz")
	fs.BoolVar(&f.encrypt, "encrypt", false, "Encrypt with a passphrase, asked for on the terminal unless --passphrase-file is given.\nExample: --encrypt")
	fs.StringVar(&f.passFile, "passphrase-file", "", "Read the passphrase from the first line of this file instead of asking. "+
		"Needs --encrypt. Passphrases are never accepted on the command line.\nExample: --passphrase-file pass.txt")
	fs.StringVar(&f.signKey, "sign-key", "", "Sign the transfer with this private key, made by qrvt keygen.\nExample: --sign-key me.key")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Print the video length, file size and expected filming time, then stop without writing.\nExample: --dry-run")
	addEngineFlags(fs, &f.engine, false)
	return cmd
}

// encode validates the flags, builds the options and runs the encode.
func (a *app) encode(cmd *cobra.Command, args []string, f *encodeFlags) error {
	o, req, err := a.encodeOptions(cmd, args, f)
	if err != nil {
		return err
	}
	info := ui.Info{Command: "encode", Sources: o.Inputs, Output: o.Output, Video: profileFields(o.Profile),
		Stages: []qrvt.Stage{qrvt.StagePack, qrvt.StageProtect, qrvt.StageRender, qrvt.StageEncodeVideo}}
	if f.dryRun {
		info.Stages = info.Stages[:2]
	}
	return a.run(cmd, info, nil, func(ctx context.Context, _ ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
		o.Progress = progress
		rep, err := a.env.API.Encode(ctx, o)
		if err != nil {
			return nil, err
		}
		return encodeSummary(rep, o, req), nil
	})
}

// encodeOptions turns the flags into options; req is the size asked for, before reduction to the cell.
func (a *app) encodeOptions(cmd *cobra.Command, args []string, f *encodeFlags) (o qrvt.EncodeOptions, req [2]int, err error) {
	if len(args) == 0 {
		return o, req, usagef("encode needs at least one file or folder")
	}
	if f.output == "" {
		return o, req, usagef("--output is required")
	}
	for _, p := range args {
		if _, err := statErr(p); err != nil {
			return o, req, usagef("input %s: %v", p, err)
		}
	}
	w, h, err := parseResolution(f.resolution)
	if err != nil {
		return o, req, usagef("--resolution %q: %v", f.resolution, err)
	}
	req = [2]int{w, h}
	p := qrvt.Profile{FPS: f.fps, ParityPercent: f.parity, Cell: f.cell}
	switch {
	case f.cell != 4 && f.cell != 8 && f.cell != 12 && f.cell != 16:
		return o, req, usagef("--cell %d: use 4, 8, 12 or 16", f.cell)
	case f.fps < 1 || f.fps > 240:
		return o, req, usagef("--fps %d: use 1 to 240", f.fps)
	case f.parity < 5 || f.parity > 100:
		return o, req, usagef("--parity %d: use 5 to 100", f.parity)
	case f.hdr && cmd.Flags().Changed("mode"):
		return o, req, usagef("--hdr selects eight greys; leave out --mode")
	case f.passFile != "" && !f.encrypt:
		return o, req, usagef("--passphrase-file needs --encrypt")
	}
	if p.Width, p.Height, err = fitCell(w, h, f.cell, "use a larger --cell"); err != nil {
		return o, req, err
	}
	if p.Modulation, err = choose("mode", f.mode, modes, "gray4, bw or color"); err != nil {
		return o, req, err
	}
	if f.hdr {
		p.Modulation = qrvt.Gray8
	}
	if p.Protection, err = choose("protection", f.protection, protections, "fast, normal or robust"); err != nil {
		return o, req, err
	}
	o = qrvt.EncodeOptions{Inputs: args, Output: f.output, Profile: p, DryRun: f.dryRun, Backend: f.backend, Workers: f.jobs}
	if o.Compression, err = choose("compress", f.compress, compressions, "auto, zstd, xz or none"); err != nil {
		return o, req, err
	}
	if err := f.check(); err != nil {
		return o, req, err
	}
	if f.signKey != "" {
		if o.SignKey, err = a.readSignKey(f.signKey); err != nil {
			return o, req, err
		}
	}
	if f.encrypt {
		pass, err := a.encryptPassphrase(f.passFile)
		if err != nil {
			return o, req, err
		}
		o.Passphrase = func() (string, error) { return pass, nil }
	}
	return o, req, nil
}

// encryptPassphrase reads the passphrase for --encrypt from a file or the terminal.
func (a *app) encryptPassphrase(file string) (string, error) {
	if file != "" {
		return readPassphraseFile(file)
	}
	return a.promptNew()
}

// profileFields describes a profile for the dashboard's video panel.
func profileFields(p qrvt.Profile) []ui.Field {
	return []ui.Field{
		{Key: "Size", Value: fmt.Sprintf("%dx%d", p.Width, p.Height)},
		{Key: "Rate", Value: fmt.Sprintf("%d fps", p.FPS)},
		{Key: "Mode", Value: fmt.Sprintf("%s, cell %d", modeName(p.Modulation), p.Cell)},
		{Key: "Protection", Value: fmt.Sprintf("%s, parity %d%%", protectionName(p.Protection), p.ParityPercent)},
	}
}

// encodeSummary describes a finished or planned encode.
func encodeSummary(rep *qrvt.EncodeReport, o qrvt.EncodeOptions, req [2]int) *ui.Summary {
	p := rep.Profile
	s := &ui.Summary{Title: "Wrote " + o.Output, Warnings: rep.Warnings}
	if rep.Output != "" {
		s.Title = "Wrote " + rep.Output
	}
	if o.DryRun {
		s.Title = "Dry run: nothing was written"
	}
	add := func(k, v string) { s.Fields = append(s.Fields, ui.Field{Key: k, Value: v}) }
	add("Video", fmt.Sprintf("%dx%d at %d fps, %s, cell %d", p.Width, p.Height, p.FPS, modeName(p.Modulation), p.Cell))
	if req != [2]int{p.Width, p.Height} {
		add("Size", fmt.Sprintf("reduced from %dx%d to a multiple of the %d-pixel cell", req[0], req[1], p.Cell))
	}
	add("Protection", fmt.Sprintf("%s, parity %d%%", protectionName(p.Protection), p.ParityPercent))
	add("Files", fmt.Sprint(rep.Files))
	add("Archive", ui.FormatBytes(rep.ArchiveLen)+" before compression")
	add("Stream", ui.FormatBytes(rep.StreamLen))
	add("Blocks", fmt.Sprintf("%d x (%d + %d)", rep.Blocks, rep.K, rep.M))
	add("Frames", fmt.Sprint(rep.Frames))
	add("Loop length", ui.FormatDuration(rep.Duration))
	size := ui.FormatBytes(rep.VideoBytes)
	if o.DryRun {
		size += ", estimated"
	}
	add("Video size", size)
	add("Filming time", ui.FormatDuration(2*rep.Duration)+": two loops; one full loop is the least that can work")
	add("Session", fmt.Sprintf("%016x", rep.Session))
	return s
}
