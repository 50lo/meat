package main

import (
	"strings"
	"testing"

	"meat.dev/meat"
)

func TestFormatBodyPlain(t *testing.T) {
	res := &meat.Result{Summary: "did a thing", SmartDiff: "@@ -1 +1 @@\n-old\n+new\n"}
	got := formatBody(res, false)
	want := "# did a thing\n\n@@ -1 +1 @@\n-old\n+new\n"
	if got != want {
		t.Errorf("formatBody(plain) =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "\x1b[") {
		t.Error("plain output must contain no ANSI escapes")
	}
}

func TestFormatBodyEmptyDiff(t *testing.T) {
	for _, color := range []bool{false, true} {
		got := formatBody(&meat.Result{Summary: "s"}, color)
		if !strings.Contains(got, "no meaningful change") {
			t.Errorf("color=%v: empty diff should say so, got %q", color, got)
		}
	}
}

func TestColorizeDiffLine(t *testing.T) {
	// Context lines (and anything not a recognized diff line) stay untouched.
	if got := colorizeDiffLine(" context"); got != " context" {
		t.Errorf("context line should be unchanged, got %q", got)
	}
	// Added/removed lines get wrapped (when git provides a color); if git is
	// unavailable diffColor returns "" and the line is returned as-is. Either
	// way the original text must be preserved.
	for _, line := range []string{"+added", "-removed", "@@ -1 +1 @@", "diff --git a/x b/x"} {
		got := colorizeDiffLine(line)
		if !strings.Contains(got, line) {
			t.Errorf("colorizeDiffLine(%q) dropped the original text: %q", line, got)
		}
		if strings.Contains(got, "\x1b[") && !strings.HasSuffix(got, ansiReset) {
			t.Errorf("colored line %q must end with a reset", got)
		}
	}
}

// TestRenderResultNonTerminalIsPlain ensures a non-*os.File writer (our test
// buffer here is exercised via formatBody; renderResult routes by isTerminal)
// never gets paged or colorized. We assert isTerminal's contract directly.
func TestIsTerminalNonFile(t *testing.T) {
	if isTerminal(&strings.Builder{}) {
		t.Error("a non-*os.File writer must not be treated as a terminal")
	}
}
