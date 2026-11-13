package cli

import (
	"context"
	"fmt"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
	"github.com/spf13/cobra"
)

// decodeFlags are the flags of qrvt decode.
type decodeFlags struct {
	output, stateDir, report, maxSize, passFile string
	verifyKeys                                  []string
	overwrite                                   bool
	engine
}

func (a *app) decodeCmd() *cobra.Command {
	var f decodeFlags
	cmd := &cobra.Command{
		Use:   "decode <recording>... -o dir",
		Short: "Recover the files from recordings of a code video",
		Long: "Read one or more recordings of a code video and write the files it carries into a folder. " +
			"Recordings of the same video can be combined: each fills in what the others missed.\n\n" +
			"Nothing is written unless every file is complete and its hash matches. Existing files are not replaced without --overwrite. " +
			"With --verify-key, the transfer must be signed by one of the keys given, or it is refused before anything is decrypted or unpacked.",
		Example: "qrvt decode IMG_0412.MOV -o received/\n" +
			"qrvt decode IMG_0412.MOV IMG_0413.MOV -o received/ --verify-key alice.pub\n" +
			"qrvt decode clip.mp4 -o out/ --state-dir .qrvt-state --report report.json",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return a.decode(cmd, args, &f) },
	}
	fs := cmd.Flags()
	fs.StringVarP(&f.output, "output", "o", "", "The folder to write the files into (required); it is created if missing.\nExample: -o received/")
	fs.StringVar(&f.passFile, "passphrase-file", "", "Read the passphrase of an encrypted transfer from the first line of this file instead of asking. "+
		"Passphrases are never accepted on the command line.\nExample: --passphrase-file pass.txt")
	fs.StringArrayVar(&f.verifyKeys, "verify-key", nil, "A trusted public key: a file made by qrvt keygen, or the key line itself. May be repeated. "+
		"When given, the transfer must be signed by one of these keys.\nExample: --verify-key alice.pub --verify-key bob.pub")
	fs.StringVar(&f.maxSize, "max-size", "16GiB", "Largest total size that may be unpacked, with a unit: B, KiB, MiB, GiB, TiB "+
		"(K, M, G, T mean the same) or kB, MB, GB, TB.\nExample: --max-size 500MB")
	fs.BoolVar(&f.overwrite, "overwrite", false, "Allow existing files in the output folder to be replaced.\nExample: --overwrite")
	fs.StringVar(&f.stateDir, "state-dir", "", "Keep progress in this folder, to resume an interrupted decode or merge recordings over several runs.\n"+
		"Example: --state-dir .qrvt-state")
	fs.StringVar(&f.report, "report", "", "Write a JSON report of the decode to this file.\nExample: --report report.json")
	addEngineFlags(fs, &f.engine, true)
	return cmd
}

// decode validates the flags, builds the options and runs the decode.
func (a *app) decode(cmd *cobra.Command, args []string, f *decodeFlags) error {
	o, err := a.decodeOptions(args, f)
	if err != nil {
		return err
	}
	pass := ""
	if f.passFile != "" {
		if pass, err = readPassphraseFile(f.passFile); err != nil {
			return err
		}
	}
	info := ui.Info{Command: "decode", Sources: o.Recordings, Output: o.OutputDir, Signal: true,
		Stages: []qrvt.Stage{qrvt.StageDecodeVideo, qrvt.StageDemodulate, qrvt.StageRepair, qrvt.StageVerify, qrvt.StageUnpack}}
	return a.run(cmd, info, &o, func(ctx context.Context, r ui.Renderer, progress func(qrvt.Event)) (*ui.Summary, error) {
		o.Progress = progress
		o.Passphrase = a.decodePassphrase(pass, func() ui.Renderer { return r })
		rep, err := a.env.API.Decode(ctx, o)
		if err != nil {
			return nil, err
		}
		return decodeSummary(rep, o), nil
	})
}

// decodeOptions turns the flags into options.
func (a *app) decodeOptions(args []string, f *decodeFlags) (o qrvt.DecodeOptions, err error) {
	if len(args) == 0 {
		return o, usagef("decode needs at least one recording")
	}
	if f.output == "" {
		return o, usagef("--output is required")
	}
	if err := checkRecordings(args); err != nil {
		return o, err
	}
	if err := checkFolder("--output", f.output); err != nil {
		return o, err
	}
	o = qrvt.DecodeOptions{Recordings: args, OutputDir: f.output, StateDir: f.stateDir, Overwrite: f.overwrite, Report: f.report,
		Backend: f.backend, Workers: f.jobs, UnsandboxedHEVC: f.hevc}
	if o.MaxSize, err = parseSize(f.maxSize); err != nil {
		return o, usagef("--max-size %q: %v", f.maxSize, err)
	}
	for _, v := range f.verifyKeys {
		k, err := readVerifyKey(v)
		if err != nil {
			return o, err
		}
		o.VerifyKeys = append(o.VerifyKeys, k)
	}
	if f.report != "" {
		if err := checkNewFile("--report", f.report); err != nil {
			return o, err
		}
	}
	if err := checkFolder("--state-dir", f.stateDir); err != nil {
		return o, err
	}
	return o, f.check()
}

// checkRecordings checks that every recording is an existing file.
func checkRecordings(paths []string) error {
	for _, p := range paths {
		if err := checkFile("recording", p); err != nil {
			return err
		}
	}
	return nil
}

// decodeSummary describes a finished decode, with the signature wording of SECURITY.md section 6.
func decodeSummary(rep *qrvt.DecodeReport, o qrvt.DecodeOptions) *ui.Summary {
	dir := rep.OutputDir
	if dir == "" {
		dir = o.OutputDir
	}
	s := &ui.Summary{Title: fmt.Sprintf("Decoded %d files (%s) into %s", rep.Files, ui.FormatBytes(rep.Bytes), dir), Warnings: rep.Warnings}
	add := func(k, v string) { s.Fields = append(s.Fields, ui.Field{Key: k, Value: v}) }
	add("Recordings", fmt.Sprintf("%d (%d captures)", len(o.Recordings), rep.Captures))
	add("Frames", fmt.Sprintf("%d of %d seen, %.1f%% clean", rep.FramesSeen, rep.FramesTotal, 100*rep.CleanFraction))
	add("Session", fmt.Sprintf("%016x", rep.Session))
	if rep.Encrypted {
		add("Encrypted", "yes, with a passphrase")
	} else {
		add("Encrypted", "no")
	}
	switch {
	case !rep.Signed:
		add("Signature", "unsigned")
	case len(o.VerifyKeys) > 0:
		add("Signature", "verified, signed by "+fingerprint(rep.SignerKey))
	default:
		add("Signature", "signed by "+fingerprint(rep.SignerKey)+"; not checked against a trusted key")
	}
	if o.Report != "" {
		add("Report", o.Report)
	}
	return s
}
