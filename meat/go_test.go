package meat

import (
	"strings"
	"testing"
)

func TestCompileEditPlan_GoStructure(t *testing.T) {
	const raw = "diff --git a/work.go b/work.go\n--- a/work.go\n+++ b/work.go\n@@ -0,0 +1,4 @@\n+func work() {\n+\tfirst()\n+\tsecond()\n+}\n"

	t.Run("rejects hidden function owner with retained body", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
		if err == nil || !strings.Contains(err.Error(), "Go") {
			t.Fatalf("hidden Go owner error = %v, want Go structural error", err)
		}
	})

	t.Run("rejects an orphaned body even when braces stay balanced", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}, {StartLine: 8, EndLine: 8}}})
		if err == nil || !strings.Contains(err.Error(), "hides Go block owner") {
			t.Fatalf("orphaned Go body error = %v", err)
		}
	})

	t.Run("allows folding a function interior", func(t *testing.T) {
		compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 7}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(compiled.smartDiff, "+func work() {\n+\t...\n+}\n") {
			t.Fatalf("Go interior fold =\n%s", compiled.smartDiff)
		}
	})

	t.Run("rejects a replacement that removes Go syntax", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
			Line: 5,
			Old:  "func work() {",
			New:  "func work...",
		}}})
		if err == nil || !strings.Contains(err.Error(), "preserve Go delimiters") {
			t.Fatalf("Go structural replacement error = %v", err)
		}
	})
}

func TestCompileEditPlan_GoStructureRejectsDanglingMultilineCall(t *testing.T) {
	const raw = "diff --git a/work.go b/work.go\n--- a/work.go\n+++ b/work.go\n@@ -0,0 +1,4 @@\n+result := call(\n+\tfirst,\n+\tsecond,\n+)\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "delimiter structure") {
		t.Fatalf("dangling Go call error = %v", err)
	}
}

func TestCompileEditPlan_GoReplacementKeepsErrorStructure(t *testing.T) {
	const raw = "diff --git a/work.go b/work.go\n--- a/work.go\n+++ b/work.go\n@@ -0,0 +1 @@\n+return fmt.Errorf(\"bad request %q for %q\", name, user)\n"
	compiled, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
		Line: 5,
		Old:  "\"bad request %q for %q\", name, user",
		New:  "\"bad request ...\", ...",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.smartDiff, "+return fmt.Errorf(\"bad request ...\", ...)") {
		t.Fatalf("Go error elision =\n%s", compiled.smartDiff)
	}
}
