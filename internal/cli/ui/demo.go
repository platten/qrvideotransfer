package ui

import (
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// DemoInfo describes the recorded decode that DemoEvents plays.
func DemoInfo() Info { return Info{} }

// DemoEvents returns a recorded decode, as events stamped from start, for the hidden demo command
// and the golden tests.
func DemoEvents(start time.Time) []qrvt.Event { return nil }

// DemoSummary is the summary that ends the demo.
func DemoSummary() *Summary { return nil }
