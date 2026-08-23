package meat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexModelGenerateReturnsValidatedSubmitCall(t *testing.T) {
	tmp := t.TempDir()
	var gotArgs []string
	var gotInput []byte
	var gotDir string
	m := &CodexModel{
		Binary:   "/usr/local/bin/codex",
		RepoRoot: tmp,
		Model:    "gpt-test",
		runner: func(_ context.Context, _ string, args []string, dir string, input []byte, _ []string, _ func()) ([]byte, []byte, error) {
			gotArgs = append([]string(nil), args...)
			gotInput = append([]byte(nil), input...)
			gotDir = dir
			var schemaPath string
			for i, arg := range args {
				if arg == "--output-schema" {
					schemaPath = args[i+1]
				}
			}
			if schemaPath == "" {
				t.Fatal("Codex command did not receive an output schema")
			}
			if _, err := os.Stat(schemaPath); err != nil {
				t.Fatalf("output schema is not available to the command: %v", err)
			}
			return []byte(`{"remove":[],"replace":[],"fold":[],"summary":"Keeps the behavioral change."}`), nil, nil
		},
	}
	resp, err := m.Generate(context.Background(), "system instructions", []Message{{
		Role:    RoleUser,
		Content: []Block{{Type: "text", Text: "numbered diff"}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != tmp {
		t.Fatalf("Codex working directory = %q, want %q", gotDir, tmp)
	}
	if !containsArgs(gotArgs, "exec") || !containsArgs(gotArgs, "--ephemeral") ||
		!containsArgs(gotArgs, "--sandbox") || !containsArgs(gotArgs, "read-only") ||
		!containsArgs(gotArgs, "--cd") || !containsArgs(gotArgs, tmp) ||
		!containsArgs(gotArgs, "--model") || !containsArgs(gotArgs, "gpt-test") ||
		!containsArgs(gotArgs, "-") {
		t.Fatalf("Codex command args = %v", gotArgs)
	}
	if !strings.Contains(string(gotInput), "system instructions") || !strings.Contains(string(gotInput), "numbered diff") {
		t.Fatalf("Codex prompt omitted transcript content: %s", gotInput)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" || resp.Content[0].ToolName != "submit" {
		t.Fatalf("Codex response = %+v, want synthetic submit tool call", resp.Content)
	}
	var plan submission
	if err := json.Unmarshal(resp.Content[0].ToolInput, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Keeps the behavioral change." {
		t.Fatalf("Codex plan summary = %q", plan.Summary)
	}
}

func TestCodexModelGenerateRejectsNonObjectOutput(t *testing.T) {
	m := &CodexModel{
		Binary: "/usr/local/bin/codex",
		runner: func(context.Context, string, []string, string, []byte, []string, func()) ([]byte, []byte, error) {
			return []byte("not JSON"), nil, nil
		},
	}
	if _, err := m.Generate(context.Background(), "system", nil, nil); err == nil || !strings.Contains(err.Error(), "decode Codex submission") {
		t.Fatalf("Generate error = %v, want structured-output decode error", err)
	}
}

func TestCodexModelGenerateReportsIdleTimeout(t *testing.T) {
	oldTimeout := codexIdleTimeout
	codexIdleTimeout = 20 * time.Millisecond
	defer func() { codexIdleTimeout = oldTimeout }()

	m := &CodexModel{
		Binary: "/usr/local/bin/codex",
		runner: func(ctx context.Context, _ string, _ []string, _ string, _ []byte, _ []string, _ func()) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}
	_, err := m.Generate(context.Background(), "system", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") || !strings.Contains(err.Error(), "meat terminated the Codex process") {
		t.Fatalf("Generate error = %v, want actionable idle-timeout error", err)
	}
}

func TestCodexModelGenerateActivityResetsIdleTimeout(t *testing.T) {
	oldTimeout := codexIdleTimeout
	codexIdleTimeout = 20 * time.Millisecond
	defer func() { codexIdleTimeout = oldTimeout }()

	m := &CodexModel{
		Binary: "/usr/local/bin/codex",
		runner: func(_ context.Context, _ string, _ []string, _ string, _ []byte, _ []string, touch func()) ([]byte, []byte, error) {
			for i := 0; i < 3; i++ {
				time.Sleep(5 * time.Millisecond)
				touch()
			}
			return []byte(`{"remove":[],"replace":[],"fold":[],"summary":"done"}`), nil, nil
		},
	}
	if _, err := m.Generate(context.Background(), "system", nil, nil); err != nil {
		t.Fatalf("Generate returned idle-timeout error despite activity: %v", err)
	}
}

func TestSanitizedCodexEnvRemovesProviderKeys(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"OPENAI_API_KEY=do-not-pass",
		"OPENAI_BASE_URL=https://proxy.invalid",
		"ANTHROPIC_API_KEY=do-not-pass",
		"ANTHROPIC_BASE_URL=https://proxy.invalid",
		"CODEX_API_KEY=do-not-pass",
		"CODEX_HOME=/tmp/codex",
	}
	got := sanitizedCodexEnv(env)
	joined := strings.Join(got, "\n")
	for _, unwanted := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CODEX_API_KEY"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("sanitized environment still contains %s: %v", unwanted, got)
		}
	}
	for _, wanted := range []string{"PATH=/bin", "CODEX_HOME=/tmp/codex"} {
		if !containsString(got, wanted) {
			t.Errorf("sanitized environment lost %s: %v", wanted, got)
		}
	}
}

func TestModelCacheIdentitySeparatesCodexAndAPI(t *testing.T) {
	t.Setenv("MEAT_MODEL", "")
	if got := ModelCacheIdentity("codex", ""); got != "codex:configured" {
		t.Fatalf("Codex default cache identity = %q", got)
	}
	if got := ModelCacheIdentity("api", ""); got != "api:"+DefaultModel {
		t.Fatalf("API default cache identity = %q", got)
	}
}

func containsArgs(args []string, want string) bool { return containsString(args, want) }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNewCodexFromEnvUsesAbsoluteBinary(t *testing.T) {
	model, err := NewCodexFromEnv(filepath.Clean("/tmp/repo"), "")
	if err != nil {
		t.Skipf("codex CLI is not installed in this test environment: %v", err)
	}
	if !filepath.IsAbs(model.Binary) {
		t.Fatalf("Codex binary = %q, want absolute path", model.Binary)
	}
}
