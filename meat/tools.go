package meat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxToolOutput bounds the size of any single tool result fed back to the model,
// so a giant file or a broad grep can't blow the context window.
const maxToolOutput = 16 * 1024

// submission is what the agent produces via the submit tool: the abridged diff
// plus a one-line summary of the change.
type submission struct {
	SmartDiff string `json:"smart_diff"`
	Summary   string `json:"summary"`
}

// toolbox holds per-run state the read-only tools need: the repo root they are
// confined to, and the submission captured by the submit tool.
type toolbox struct {
	root       string
	submitted  *submission
	submitSeen bool
}

// submitTool is always advertised; read_file/grep are only offered when the
// toolbox is confined to a repo root.
func (tb *toolbox) submitTool() Tool {
	return Tool{
		Name:        "submit",
		Description: "Submit the final abridged reading diff. Call this exactly once when done. smart_diff is the abridged unified diff (empty if nothing meaningful changed); summary is a one-line description of the change.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"smart_diff":{"type":"string","description":"The abridged unified diff. May be empty when no meaningful change remains."},"summary":{"type":"string","description":"One-line, high-level description of what the change does."}},"required":["smart_diff","summary"]}`),
	}
}

// tools returns the tool schemas advertised to the model.
func (tb *toolbox) tools() []Tool {
	if tb.root == "" {
		return []Tool{tb.submitTool()}
	}
	return []Tool{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file from the repository to gather clues about whether a diff line is load-bearing (or whether a file is generated). Paths are relative to the repo root. Optionally restrict to an inclusive 1-based line range with start_line/end_line.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to the repo root."},"start_line":{"type":"integer","description":"1-based first line (optional)."},"end_line":{"type":"integer","description":"1-based last line (optional)."}},"required":["path"]}`),
		},
		{
			Name:        "grep",
			Description: "Search the repository for a regular expression (git grep). Use it to find call sites, type definitions, generator directives, or whether a symbol is used elsewhere. Optionally scope to a path prefix.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression to search for."},"path":{"type":"string","description":"Optional path prefix (relative to repo root)."}},"required":["pattern"]}`),
		},
		tb.submitTool(),
	}
}

// run dispatches a tool call by name and returns the textual result plus whether
// it was an error.
func (tb *toolbox) run(ctx context.Context, name string, input json.RawMessage) (string, bool) {
	switch name {
	case "read_file":
		return tb.readFile(input)
	case "grep":
		return tb.grep(ctx, input)
	case "submit":
		return tb.submit(input)
	default:
		return fmt.Sprintf("unknown tool %q", name), true
	}
}

// resolveInRoot joins rel to the repo root and verifies the result stays inside
// it, defeating path traversal via .. or absolute paths.
func (tb *toolbox) resolveInRoot(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be relative to the repo root")
	}
	rootAbs, err := filepath.Abs(tb.root)
	if err != nil {
		return "", err
	}
	absAbs, err := filepath.Abs(filepath.Join(tb.root, clean))
	if err != nil {
		return "", err
	}
	if absAbs != rootAbs && !strings.HasPrefix(absAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the repo root")
	}
	return absAbs, nil
}

type readFileInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (tb *toolbox) readFile(raw json.RawMessage) (string, bool) {
	var in readFileInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}
	abs, err := tb.resolveInRoot(in.Path)
	if err != nil {
		return err.Error(), true
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("read %s: %v", in.Path, err), true
	}
	text := string(data)
	if in.StartLine > 0 || in.EndLine > 0 {
		text = sliceLines(text, in.StartLine, in.EndLine)
	}
	return truncateForTool(text), false
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (tb *toolbox) grep(ctx context.Context, raw json.RawMessage) (string, bool) {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "pattern is required", true
	}
	args := []string{"grep", "-n", "-I", "--no-color", "-e", in.Pattern}
	if in.Path != "" {
		if _, err := tb.resolveInRoot(in.Path); err != nil {
			return err.Error(), true
		}
		args = append(args, "--", in.Path)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = tb.root
	out, err := cmd.Output()
	if len(out) == 0 {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "(no matches)", false
		}
		if err != nil {
			return fmt.Sprintf("git grep: %v", err), true
		}
		return "(no matches)", false
	}
	return truncateForTool(capLines(string(out), 200)), false
}

func (tb *toolbox) submit(raw json.RawMessage) (string, bool) {
	var in submission
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}
	tb.submitted = &submission{SmartDiff: in.SmartDiff, Summary: in.Summary}
	tb.submitSeen = true
	return "Submitted.", false
}

func sliceLines(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return ""
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return b.String()
}

func capLines(s string, max int) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	n := 0
	for sc.Scan() {
		if n >= max {
			fmt.Fprintf(&b, "... (truncated, more than %d lines)\n", max)
			break
		}
		b.WriteString(sc.Text())
		b.WriteByte('\n')
		n++
	}
	return b.String()
}

func truncateForTool(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + fmt.Sprintf("\n... (truncated, %d total bytes)", len(s))
}
