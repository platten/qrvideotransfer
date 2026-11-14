package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestManKeepsPercent checks that a % in any help text reaches the man page literally: the roff
// generator uses text as a format string.
func TestManKeepsPercent(t *testing.T) {
	root := &cobra.Command{Use: "tool", Short: "Short 10% text", Long: "Long 20% text.\n* list 30% item\nAfter 40% list"}
	sub := &cobra.Command{Use: "sub", Short: "Sub 50% short", Example: "tool sub --n 60%", Run: func(*cobra.Command, []string) {}}
	sub.Flags().Int("n", 0, "Usage 70% of it; 80%s and %d and %%")
	sub.Flags().Int("level", 5, "A level")
	sub.Flags().Bool("on", false, "A switch")
	root.AddCommand(sub)
	out, err := manPage(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"10%", "20%", "30%", "40%", "50%", "70%", "80%s and %d and %%", "tool sub --n 60%", "A level (default: 5)"} {
		if !strings.Contains(out, want) {
			t.Errorf("man page lacks %q", want)
		}
	}
	if strings.Contains(out, "MISSING") || strings.Contains(out, "%!") {
		t.Errorf("garbled man page:\n%s", out)
	}
	if strings.Contains(out, "A switch (default") || strings.Contains(out, "of it; 80%s and %d and %% (default") {
		t.Error("a default is shown for a flag whose default is false or 0")
	}
}

func TestManCommandKeepsPercent(t *testing.T) {
	out, _, err := run(t, &fakeAPI{}, nil, "man")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"83% of the loop", "5% steps", "qrvt man > qrvt\\&.1", "(default: 1080p)", "qrvt decode IMG_0412\\&.MOV -o received/"} {
		if !strings.Contains(out, want) {
			t.Errorf("qrvt man lacks %q", want)
		}
	}
	if strings.Contains(out, "MISSING") || strings.Contains(out, "%!") {
		t.Error("qrvt man output is garbled")
	}
}
