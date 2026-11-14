package cli

import (
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
	return page.Build(roff.NewDocument()), nil
}
