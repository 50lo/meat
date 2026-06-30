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
// configured diff colors (honoring color.ui/color.diff) and page through git's
// pager. Otherwise it writes plain text (so pipes and redirects stay clean).
func renderResult(w io.Writer, res *meat.Result) {
	tty := isTerminal(w)
	color := tty && gitWantsColor(tty)
	body := formatBody(res, palette(color))
	if !tty {
		io.WriteString(w, body)
		return
	}
	if err := page(body); err != nil {
		// Pager unavailable/failed: fall back to writing directly.
		io.WriteString(w, body)
	}
}

const ansiReset = "\x1b[m"

// diffPalette holds the ANSI escapes for each unified-diff line kind, resolved
// once per render (git's color.diff.<slot>). Empty strings mean "no color".
type diffPalette struct {
	meta, frag, old, new string
}

// palette resolves the diff colors. When color is false it returns the zero
// palette (everything plain), so a single bool fully controls colorization.
func palette(color bool) diffPalette {
	if !color {
		return diffPalette{}
	}
	return diffPalette{
		meta: diffColor("meta", "bold"),
		frag: diffColor("frag", "cyan"),
		old:  diffColor("old", "red"),
		new:  diffColor("new", "green"),
	}
}

// formatBody renders summary + diff to a string using the given palette.
func formatBody(res *meat.Result, p diffPalette) string {
	var b strings.Builder
	if res.Summary != "" {
		// git paints the commit header line; reuse the "frag"/meta family. We
		// use a dedicated commit color (yellow) like `git show`.
		if c := commitColor(p); c != "" {
			fmt.Fprintf(&b, "%s# %s%s\n\n", c, res.Summary, ansiReset)
		} else {
			fmt.Fprintf(&b, "# %s\n\n", res.Summary)
		}
	}
	diff := strings.TrimRight(res.SmartDiff, "\n")
	if strings.TrimSpace(diff) == "" {
		b.WriteString("(no meaningful change to read)\n")
		return b.String()
	}
	for _, line := range strings.Split(diff, "\n") {
		b.WriteString(colorizeDiffLine(line, p))
		b.WriteString("\n")
	}
	return b.String()
}

// commitColor returns the color for the summary header. It's only colored when
// the palette is active (some slot is non-empty).
func commitColor(p diffPalette) string {
	if p == (diffPalette{}) {
		return ""
	}
	return diffColor("commit", "yellow")
}

// colorizeDiffLine wraps a unified-diff line in the palette color for its kind,
// matching how `git show` paints a diff. An empty palette returns the line
// unchanged. Metadata classification mirrors git's diff metadata lines
// (headers, mode/rename/copy/similarity), so all of them get the meta color.
func colorizeDiffLine(line string, p diffPalette) string {
	var c string
	switch {
	case isDiffMeta(line):
		c = p.meta
	case strings.HasPrefix(line, "@@"):
		c = p.frag
	case strings.HasPrefix(line, "+"):
		c = p.new
	case strings.HasPrefix(line, "-"):
		c = p.old
	default:
		return line // context line: no color
	}
	if c == "" {
		return line
	}
	return c + line + ansiReset
}

// isDiffMeta reports whether line is git diff metadata (painted with the meta
// color by `git show`). "+++"/"---" file headers count as meta, but they must be
// checked before the generic +/- line coloring.
func isDiffMeta(line string) bool {
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return true
	}
	for _, p := range diffMetaPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// diffMetaPrefixes are the non-+++/--- metadata line starts git colors as meta.
var diffMetaPrefixes = []string{
	"diff ",
	"index ",
	"old mode ",
	"new mode ",
	"new file mode ",
	"deleted file mode ",
	"similarity index ",
	"dissimilarity index ",
	"rename from ",
	"rename to ",
	"copy from ",
	"copy to ",
}

// gitWantsColor asks git whether color.diff should be enabled, given whether
// stdout is a tty. This honors color.ui / color.diff = always/auto/never just
// like `git show` does, rather than always coloring on a tty.
func gitWantsColor(tty bool) bool {
	arg := "false"
	if tty {
		arg = "true"
	}
	// With an explicit stdout-is-tty argument, --get-colorbool prints
	// "true"/"false" and exits 0; we parse the text. (The exit-status form only
	// applies when the argument is omitted, in which case git probes its own
	// stdout, which here is a pipe.)
	out, err := exec.Command("git", "config", "--get-colorbool", "color.diff", arg).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
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

// isTerminal reports whether w is an interactive terminal. It uses a real
// isatty(3) ioctl rather than a char-device check, so redirecting to /dev/null
// (also a char device) is correctly treated as non-interactive, matching git.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty(f.Fd())
}
