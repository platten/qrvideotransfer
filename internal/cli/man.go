package cli

import (
	"strings"

	mcobra "github.com/muesli/mango-cobra"
	"github.com/muesli/roff"
	"github.com/spf13/cobra"
)

// manPage renders the manual page of a command tree.
func manPage(root *cobra.Command) (string, error) {
	page, err := mcobra.NewManPage(1, root)
	if err != nil {
		return "", err
	}
	return page.Build(percentSafe{roff.NewDocument()}), nil
}

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
