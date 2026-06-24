// Package meat abridges a code diff down to the parts a senior reviewer
// actually needs to read.
//
// Reviewing good code in diff form is mostly about understanding the high-level
// change to the program, not sweating mechanical details: the code compiles and
// its tests pass. A reviewer does not need to read the exact spelling of an
// error message, an obvious zero value added to a return, the type conversions
// threaded through a field copy — or generated code. They need to know "what
// changed, where did it come from, where did it go".
//
// meat takes a whole unified diff (spanning as many files as it touches) and
// asks an LLM agent to produce a "reading diff": the same change, rewritten to
// elide the noise and keep only what carries meaning. The agent has read-only
// access to the surrounding source tree so it can use clues to decide what is
// load-bearing.
//
// The package is provider-agnostic: callers supply a Model. The command
// meat.dev ships a built-in Anthropic Model; embedders such as Shelley adapt
// their own LLM client.
package meat

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// defaultMaxTurns bounds how many model round-trips a single abridgement may
// take. A whole-diff run may explore several files before submitting, so this
// allows a good deal of tool use while still terminating a runaway loop.
const defaultMaxTurns = 24

// defaultBudget bounds total wall-clock time for one Abridge call so a stuck
// run can't hang forever.
const defaultBudget = 4 * time.Minute

// Request is a whole-diff abridgement request. The diff may span many files;
// abridging the whole change at once (rather than file by file) lets the model
// reason across files and gives it maximum context to decide what is
// load-bearing.
type Request struct {
	// RepoRoot is the directory the read-only tools are confined to. If empty,
	// the tools are disabled (the model abridges from the diff text alone).
	RepoRoot string
	// UnifiedDiff is the raw unified diff to abridge (e.g. the full output of
	// `git diff` or `git show`). Required.
	UnifiedDiff string
	// MaxTurns overrides defaultMaxTurns when > 0.
	MaxTurns int
}

// Result is the abridged reading diff.
type Result struct {
	// SmartDiff is the abridged unified diff. Empty when no meaningful change
	// remains after abridging.
	SmartDiff string
	// Summary is a one-line, high-level description of the change.
	Summary string
	// InputTokens and OutputTokens are the cumulative token usage across the run.
	InputTokens  int
	OutputTokens int
}

// Abridge runs the agent loop that turns req.UnifiedDiff into a reading diff,
// using the supplied Model for all generation.
func Abridge(ctx context.Context, model Model, req Request) (*Result, error) {
	if model == nil {
		return nil, fmt.Errorf("meat: nil model")
	}
	if strings.TrimSpace(req.UnifiedDiff) == "" {
		return &Result{Summary: "No changes."}, nil
	}

	maxTurns := req.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	ctx, cancel := context.WithTimeout(ctx, defaultBudget)
	defer cancel()

	tb := &toolbox{root: req.RepoRoot}
	tools := tb.tools()

	messages := []Message{{
		Role:    RoleUser,
		Content: []Block{textBlock(buildUserPrompt(req))},
	}}

	var inTok, outTok int

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := model.Generate(ctx, systemPrompt, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("meat: model generate: %w", err)
		}
		inTok += resp.InputTokens
		outTok += resp.OutputTokens

		messages = append(messages, Message{Role: RoleAssistant, Content: resp.Content})

		var results []Block
		for _, b := range resp.Content {
			if b.Type != "tool_use" {
				continue
			}
			out, isErr := tb.run(ctx, b.ToolName, b.ToolInput)
			results = append(results, Block{
				Type:       "tool_result",
				ToolUseID:  b.ID,
				ToolResult: out,
				ToolError:  isErr,
			})
		}

		if tb.submitSeen {
			return &Result{
				SmartDiff:    tb.submitted.SmartDiff,
				Summary:      tb.submitted.Summary,
				InputTokens:  inTok,
				OutputTokens: outTok,
			}, nil
		}

		if len(results) == 0 {
			// The model produced text but no tool call. Nudge it once toward
			// submitting; the loop bound prevents this from running away.
			messages = append(messages, Message{
				Role:    RoleUser,
				Content: []Block{textBlock("Call the submit tool with your abridged diff and one-line summary. If nothing meaningful changed, submit an empty smart_diff and say so in the summary.")},
			})
			continue
		}
		messages = append(messages, Message{Role: RoleUser, Content: results})
	}

	return nil, fmt.Errorf("meat: agent did not submit within %d turns", maxTurns)
}

func buildUserPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("Abridge the following unified diff into a reading diff. The diff may span multiple files; reason across them and keep the per-file structure (diff/--- /+++ headers) so the reviewer can tell which file each hunk belongs to.\n")
	if req.RepoRoot != "" {
		b.WriteString("Use read_file/grep on the surrounding source only when it changes your judgment about what is load-bearing (or whether a file is generated), then call submit.\n")
	} else {
		b.WriteString("Judge from the diff text alone, then call submit.\n")
	}
	b.WriteString("\n```diff\n")
	b.WriteString(req.UnifiedDiff)
	if !strings.HasSuffix(req.UnifiedDiff, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}
