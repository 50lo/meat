package meat

import (
	"fmt"
	"strings"
)

// ElisionLine returns a one-line, machine-computed manifest of how much of the
// raw diff survived abridging, e.g.
//
//	kept 12/240 changed lines in 3/7 files
//
// It is computed locally by counting +/- lines and per-file headers in both
// diffs — the LLM has no say in it — so the reviewer can always see, at a
// glance, how much they are NOT reading. When the abridged diff kept no
// per-file headers the file counts are omitted.
func ElisionLine(raw, abridged string) string {
	rawLines, rawFiles := diffStats(raw)
	keptLines, keptFiles := diffStats(abridged)
	if rawLines == 0 {
		return ""
	}
	if strings.TrimSpace(abridged) == "" {
		return fmt.Sprintf("elided all %d changed lines in %d files", rawLines, rawFiles)
	}
	if keptFiles > 0 && rawFiles > 0 {
		return fmt.Sprintf("kept %d/%d changed lines in %d/%d files", keptLines, rawLines, keptFiles, rawFiles)
	}
	return fmt.Sprintf("kept %d/%d changed lines", keptLines, rawLines)
}

// diffStats counts changed (+/-) lines — excluding the +++/--- file headers —
// and per-file "diff " headers in a unified diff.
func diffStats(diff string) (changed, files int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// file header, not a change
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			changed++
		case strings.HasPrefix(line, "diff "):
			files++
		}
	}
	return changed, files
}
