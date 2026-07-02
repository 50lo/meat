package meat

import (
	"strings"
	"testing"
)

const rawTwoFiles = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 package a
-old1
+new1
+new2
diff --git a/b.pb.go b/b.pb.go
--- a/b.pb.go
+++ b/b.pb.go
@@ -1,2 +1,3 @@
+gen1
+gen2
`

func TestElisionLine(t *testing.T) {
	abridged := `diff --git a/a.go b/a.go
@@ -1,3 +1,4 @@
+new1
`
	got := ElisionLine(rawTwoFiles, abridged)
	// raw: 5 changed lines (old1,new1,new2,gen1,gen2) in 2 files; kept 1 line in 1 file.
	want := "kept 1/5 changed lines in 1/2 files"
	if got != want {
		t.Errorf("ElisionLine = %q, want %q", got, want)
	}
}

func TestElisionLine_AllElided(t *testing.T) {
	got := ElisionLine(rawTwoFiles, "")
	if !strings.Contains(got, "elided all 5 changed lines") {
		t.Errorf("ElisionLine(empty) = %q, want 'elided all 5 changed lines...'", got)
	}
}

func TestElisionLine_NoFileHeadersInAbridged(t *testing.T) {
	// The model kept hunks but dropped the per-file headers: fall back to
	// line counts only, rather than claiming "0 files".
	got := ElisionLine(rawTwoFiles, "@@ -1 +1 @@\n+new1\n")
	want := "kept 1/5 changed lines"
	if got != want {
		t.Errorf("ElisionLine = %q, want %q", got, want)
	}
}

func TestElisionLine_FileHeadersNotCounted(t *testing.T) {
	// +++/--- headers must not count as changed lines.
	changed, files := diffStats(rawTwoFiles)
	if changed != 5 || files != 2 {
		t.Errorf("diffStats = (%d, %d), want (5, 2)", changed, files)
	}
}
