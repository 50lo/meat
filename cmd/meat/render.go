package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"meat.dev/meat"
)

// renderResult writes the result body (summary + diff) to w. When w is an
// interactive terminal it mimics `git show`: colorize the diff with git's
// configured diff colors and page through git's pager. Otherwise it writes
// plain text (so pipes and redirects stay clean).
func renderResult(w io.Writer, res *meat.Result) {
	body := formatBody(res, isTerminal(w))
	if !isTerminal(w) {
		io.WriteString(w, body)
		return
	}
	if err := page(body); err != nil {
		// Pager unavailable/failed: fall back to writing directly.
		io.WriteString(w, body)
	}
}

// formatBody renders summary + diff to a string, optionally with ANSI color.
func formatBody(res *meat.Result, color bool) string {
	var b strings.Builder
	if res.Summary != "" {
		if color {
			fmt.Fprintf(&b, "%s# %s%s\n\n", diffColor("commit", "yellow"), res.Summary, ansiReset)
		} else {
			fmt.Fprintf(&b, "# %s\n\n", res.Summary)
		}
	}
	diff := strings.TrimRight(res.SmartDiff, "\n")
	if strings.TrimSpace(diff) == "" {
		b.WriteString("(no meaningful change to read)\n")
		return b.String()
	}
	if !color {
		b.WriteString(diff)
		b.WriteString("\n")
		return b.String()
	}
	for _, line := range strings.Split(diff, "\n") {
		b.WriteString(colorizeDiffLine(line))
		b.WriteString("\n")
	}
	return b.String()
}

const ansiReset = "\x1b[m"

// colorizeDiffLine wraps a single unified-diff line in git's configured color
// for its kind, matching how `git show` paints a diff.
func colorizeDiffLine(line string) string {
	var slot, def string
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
		strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
		slot, def = "meta", "bold"
	case strings.HasPrefix(line, "@@"):
		slot, def = "frag", "cyan"
	case strings.HasPrefix(line, "+"):
		slot, def = "new", "green"
	case strings.HasPrefix(line, "-"):
		slot, def = "old", "red"
	default:
		return line // context line: no color
	}
	c := diffColor(slot, def)
	if c == "" {
		return line
	}
	return c + line + ansiReset
}

// diffColor returns the ANSI escape git would use for color.diff.<slot>,
// honoring the user's git config (falling back to def). Empty if git can't be
// consulted.
func diffColor(slot, def string) string {
	out, err := exec.Command("git", "config", "--get-color", "color.diff."+slot, def).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// page sends text through git's pager (the same resolution `git show` uses:
// GIT_PAGER, core.pager, $PAGER, then less). If the resolved pager is "cat" or
// empty, it writes straight to stdout.
func page(text string) error {
	pager := gitPager()
	if pager == "" || pager == "cat" {
		_, err := io.WriteString(os.Stdout, text)
		return err
	}
	cmd := exec.Command("sh", "-c", pager)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// less needs raw color passthrough and to quit if the content fits one
	// screen; this matches git's own LESS defaults.
	if _, ok := os.LookupEnv("LESS"); !ok {
		cmd.Env = append(os.Environ(), "LESS=FRX")
	}
	return cmd.Run()
}

// gitPager returns the effective pager git would use, via `git var GIT_PAGER`.
func gitPager() string {
	out, err := exec.Command("git", "var", "GIT_PAGER").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isTerminal reports whether w is an interactive terminal (os.Stdout backed by
// a char device). Non-*os.File writers (e.g. test buffers) are never terminals.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
