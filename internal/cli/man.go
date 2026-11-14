package cli

import (
	"strings"

	mcobra "github.com/muesli/mango-cobra"
	"github.com/muesli/roff"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// manPage renders the manual page of a command tree. Flag descriptions carry their defaults, as in
// --help; the generator has no field for them, so they are appended to the usage text while the
// page is built and removed afterwards.
func manPage(root *cobra.Command) (string, error) {
	var restore []func()
	defer func() {
		for _, r := range restore {
			r()
		}
	}()
	seen := map[*pflag.Flag]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				if seen[f] || !shownDefault(f.DefValue) {
					return
				}
				seen[f] = true
				usage := f.Usage
				f.Usage += " (default: " + f.DefValue + ")"
				restore = append(restore, func() { f.Usage = usage })
			})
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	page, err := mcobra.NewManPage(1, root)
	if err != nil {
		return "", err
	}
	return page.Build(percentSafe{roff.NewDocument()}), nil
}

// shownDefault reports whether --help shows a flag default (fang's rule): not for empty, false,
// zero or empty-list defaults.
func shownDefault(v string) bool { return v != "" && v != "false" && v != "0" && v != "[]" }

// percentSafe passes text to a roff document with each % doubled. roff writes plain text lines
// through fmt.Fprintf as the format string, and list items (lines starting with "*") as an
// argument, so only the former are escaped.
type percentSafe struct{ *roff.Document }

// Text writes text.
func (p percentSafe) Text(text string) { p.Document.Text(escapePercent(text)) }

// TextBold writes text in bold.
func (p percentSafe) TextBold(text string) { p.Document.TextBold(escapePercent(text)) }

// TextItalic writes text in italic.
func (p percentSafe) TextItalic(text string) { p.Document.TextItalic(escapePercent(text)) }

// escapePercent doubles every % on the lines roff uses as format strings.
func escapePercent(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "*") {
			lines[i] = strings.ReplaceAll(l, "%", "%%")
		}
	}
	return strings.Join(lines, "\n")
}
