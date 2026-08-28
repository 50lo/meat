package meat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptGoldenCommits(t *testing.T) {
	const dir = "testdata/typescript"
	raw := mustReadTestFile(t, filepath.Join(dir, "ts-result-warnings.diff"))
	if changed, _ := diffStats(raw); changed != 118 {
		t.Fatalf("raw changed rows = %d, want 118", changed)
	}
	if got := len(splitSourceLines(raw)); got != 198 {
		t.Fatalf("raw physical rows = %d, want 198", got)
	}
	if len(raw) != 6190 {
		t.Fatalf("raw bytes = %d, want 6190", len(raw))
	}

	var plan editPlan
	if err := json.Unmarshal([]byte(mustReadTestFile(t, filepath.Join(dir, "ts-result-warnings.plan.json"))), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Remove == nil || plan.Replace == nil || plan.Fold == nil {
		t.Fatal("golden plan arrays must be non-null")
	}
	assertGoldenPlanLeavesImportsAutomatic(t, raw, plan)
	compiled, err := compileEditPlan(raw, plan)
	if err != nil {
		t.Fatalf("compile TypeScript golden plan: %v", err)
	}
	if compiled.stats.foldCount != 6 {
		t.Fatalf("fold count = %d, want 6", compiled.stats.foldCount)
	}
	if compiled.stats.visibleChanged > 60 {
		t.Fatalf("visible changed rows = %d, budget 60", compiled.stats.visibleChanged)
	}
	if rows := len(splitSourceLines(compiled.smartDiff)); rows > 140 {
		t.Fatalf("visible physical rows = %d, budget 140", rows)
	}
	if len(compiled.smartDiff) > 4500 {
		t.Fatalf("visible bytes = %d, budget 4500", len(compiled.smartDiff))
	}
	for _, want := range []string{
		"Result<T, E>", "warnings", "retryable", "exponential backoff",
		"TypeError is not retryable", "setWarnings",
	} {
		if !strings.Contains(compiled.smartDiff, want) {
			t.Errorf("golden missing semantic anchor %q", want)
		}
	}
	for _, unwanted := range []string{
		`import { errorMessage }`, `import type { Logger }`, `import { log }`,
		`from "./result"`, "baseOptions",
	} {
		if strings.Contains(compiled.smartDiff, unwanted) {
			t.Errorf("golden retained import or mechanical noise %q", unwanted)
		}
	}

	goldenPath := filepath.Join(dir, "ts-result-warnings.golden.diff")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(compiled.smartDiff), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustReadTestFile(t, goldenPath); got != compiled.smartDiff {
		t.Fatalf("reading diff does not match %s; run UPDATE_GOLDEN=1 go test ./meat -run TestTypeScriptGoldenCommits", goldenPath)
	}
}
