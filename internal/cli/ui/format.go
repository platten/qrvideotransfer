package ui

import (
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// StageName is the stable name of a stage, as the JSON events carry it.
func StageName(s qrvt.Stage) string { return "" }

// KindName is the stable name of an event kind.
func KindName(k qrvt.EventKind) string { return "" }

// UnitName is the stable name of a unit; "" for none.
func UnitName(u qrvt.Unit) string { return "" }

// FormatBytes formats a byte count with binary units, for example "1.5 MiB".
func FormatBytes(n int64) string { return "" }

// FormatDuration formats a duration as h:mm:ss or m:ss, rounded to the second.
func FormatDuration(d time.Duration) string { return "" }

// formatRate formats r units per second.
func formatRate(u qrvt.Unit, r float64) string { return "" }

// formatAmount formats n units.
func formatAmount(u qrvt.Unit, n int64) string { return "" }
