package implement

import "testing"

// The stop line is the skill's, with a model's typography forgiven: a
// heading's marks, emphasis, case, a byte-order mark and blank lines before it.
// Anything else first is not a stop.
func TestGapsReadsTheStopLineWhateverItsTypography(t *testing.T) {
	for report, want := range map[string]string{
		"Stopped on gaps: nothing changed.\n\n1. Q? Recommended: A.\n":          "1. Q? Recommended: A.",
		"\uFEFF\n\n## Stopped on gaps: nothing changed.\n1. Q? Recommended: A.": "1. Q? Recommended: A.",
		"**stopped on gaps: nothing changed.**\r\n- Q? Recommended: A.\r\n":     "- Q? Recommended: A.",
		"## Start here\n\nStopped on gaps: nothing changed.\n1. Q?\n":           "",
		"I stopped on gaps.\n1. Q?\n":                                           "",
		"Stopped on gaps: nothing changed.\n\n":                                 "",
	} {
		got, ok := gaps(report)
		if got != want || ok != (want != "") {
			t.Errorf("gaps(%q) = %q, %v; want %q", report, got, ok, want)
		}
	}
}
