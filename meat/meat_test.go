package meat

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedModel returns a pre-baked response per turn, letting tests drive the
// agent loop deterministically. It records the requests it received.
type scriptedModel struct {
	turns     []*Response
	seen      int
	lastTools []Tool
}

func (m *scriptedModel) Generate(_ context.Context, _ string, _ []Message, tools []Tool) (*Response, error) {
	m.lastTools = tools
	m.seen++
	if m.seen > len(m.turns) {
		return m.turns[len(m.turns)-1], nil
	}
	return m.turns[m.seen-1], nil
}

func toolUse(id, name string, input any) Block {
	raw, _ := json.Marshal(input)
	return Block{Type: "tool_use", ID: id, ToolName: name, ToolInput: raw}
}

func assistant(content ...Block) *Response {
	return &Response{Content: content, InputTokens: 100, OutputTokens: 20}
}

// gitRepo creates a throwaway git repo so the grep tool (git grep) works.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t.test")
	run("config", "user.name", "t")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

func TestAbridge_ReadThenSubmit(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"route.go": "package route\n\ntype routeData struct{ boxID int }\n",
	})

	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("t1", "read_file", readFileInput{Path: "route.go"})),
		assistant(toolUse("t2", "submit", submission{
			SmartDiff: "+    resp.SSHKeyID, UserID, BoxID = rd...\n",
			Summary:   "Copies cache fields from rd to resp.",
		})),
	}}

	res, err := Abridge(context.Background(), m, Request{
		RepoRoot:    repo,
		UnifiedDiff: "diff --git a/route.go b/route.go\n@@\n+    resp.BoxID = int64(rd.boxID)\n",
	})
	if err != nil {
		t.Fatalf("Abridge: %v", err)
	}
	if !strings.Contains(res.SmartDiff, "rd...") {
		t.Errorf("smart diff = %q, want collapsed form", res.SmartDiff)
	}
	if res.Summary == "" {
		t.Errorf("want non-empty summary")
	}
	if res.InputTokens == 0 || res.OutputTokens == 0 {
		t.Errorf("want token usage accumulated, got in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
	if m.seen != 2 {
		t.Errorf("want 2 model calls, got %d", m.seen)
	}
}

func TestAbridge_EmptyDiffShortCircuits(t *testing.T) {
	m := &scriptedModel{turns: []*Response{assistant()}}
	res, err := Abridge(context.Background(), m, Request{RepoRoot: t.TempDir(), UnifiedDiff: "  "})
	if err != nil {
		t.Fatal(err)
	}
	if res.SmartDiff != "" {
		t.Errorf("want empty smart diff")
	}
	if m.seen != 0 {
		t.Errorf("want no model calls for empty diff, got %d", m.seen)
	}
}

func TestAbridge_NoRepoOffersOnlySubmit(t *testing.T) {
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("t1", "submit", submission{SmartDiff: "", Summary: "noop"})),
	}}
	_, err := Abridge(context.Background(), m, Request{UnifiedDiff: "diff --git a/x b/x\n@@\n+x\n"})
	if err != nil {
		t.Fatal(err)
	}
	// With no RepoRoot, only the submit tool should be advertised.
	if len(m.lastTools) != 1 || m.lastTools[0].Name != "submit" {
		t.Errorf("want only submit tool, got %+v", m.lastTools)
	}
}

func TestToolboxGrepAndPathConfinement(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.go": "package a\nfunc Foo() {}\n"})
	tb := &toolbox{root: repo}

	in, _ := json.Marshal(grepInput{Pattern: "func Foo"})
	out, isErr := tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("grep error: %s", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("grep result = %q, want a.go match", out)
	}

	bad, _ := json.Marshal(readFileInput{Path: "../../../etc/passwd"})
	if _, isErr := tb.readFile(bad); !isErr {
		t.Errorf("want path traversal rejected")
	}
}

// TestAbridge_RejectsOversizeDiff: a diff over the size cap must be refused up
// front with actionable advice, never sent to the model.
func TestAbridge_RejectsOversizeDiff(t *testing.T) {
	m := &scriptedModel{turns: []*Response{assistant()}}
	big := "diff --git a/x b/x\n+" + strings.Repeat("x", maxDiffBytes)
	_, err := Abridge(context.Background(), m, Request{UnifiedDiff: big})
	if err == nil {
		t.Fatal("want error for oversize diff")
	}
	if !strings.Contains(err.Error(), "narrower") {
		t.Errorf("error should advise narrowing the range: %v", err)
	}
	if m.seen != 0 {
		t.Errorf("oversize diff must not reach the model; got %d calls", m.seen)
	}
}

// TestAbridge_ProgressCallbacks: the Progress hook receives a turn update and a
// message per tool call, so an interactive caller can show liveness.
func TestAbridge_ProgressCallbacks(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.go": "package a\n"})
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("t1", "read_file", readFileInput{Path: "a.go"})),
		assistant(toolUse("t2", "grep", grepInput{Pattern: "package"})),
		assistant(toolUse("t3", "submit", submission{SmartDiff: "", Summary: "s"})),
	}}
	var msgs []string
	_, err := Abridge(context.Background(), m, Request{
		RepoRoot:    repo,
		UnifiedDiff: "diff --git a/a.go b/a.go\n@@\n+x\n",
		Progress:    func(msg string) { msgs = append(msgs, msg) },
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"turn 1", "read_file a.go", `grep "package"`, "submitting"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress messages missing %q:\n%s", want, joined)
		}
	}
}
