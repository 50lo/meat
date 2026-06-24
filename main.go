// Command meat abridges a code diff into a "reading diff": the same change,
// rewritten to keep only what a senior reviewer actually needs to read —
// mechanical noise (batch field copies, error-message construction, forced
// zero-value returns, generated code) elided, behavior-bearing changes kept.
//
// Usage:
//
//	# summarize the most recent commit in the current repo
//	meat
//
//	# abridge any diff piped on stdin
//	git show <sha> | meat
//	git diff main...HEAD | meat
//
// It reads ANTHROPIC_API_KEY from the environment (optionally ANTHROPIC_BASE_URL
// and MEAT_MODEL / -model).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"meat.dev/meat"
)

const usage = `meat — abridge a diff into a "reading diff"

Usage:
  meat                 Summarize the most recent commit (HEAD) in the current git repo.
  git show <sha> | meat   Abridge the diff piped on stdin.
  git diff | meat         Abridge the working-tree diff piped on stdin.

meat reads a unified diff (from stdin, or HEAD when stdin is a terminal), asks an
LLM to drop everything not worth reading, and prints the abridged diff plus a
one-line summary.

Flags:
  -model string   Model to use (default $MEAT_MODEL or a built-in default).
  -h, --help      Show this help.

Environment:
  ANTHROPIC_API_KEY    Required. API key for the built-in Anthropic backend.
  ANTHROPIC_BASE_URL   Optional. Override the API base URL.
  MEAT_MODEL           Optional. Default model id.
`

func main() {
	fs := flag.NewFlagSet("meat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	model := fs.String("model", "", "model to use (default $MEAT_MODEL or built-in default)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		// flag already printed the error and (for -h) the usage.
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}

	diff, source, err := readDiff()
	if err != nil {
		fatal("%v", err)
	}
	if strings.TrimSpace(diff) == "" {
		fatal("no diff to read (%s)", source)
	}

	m, err := meat.NewAnthropicFromEnv(*model)
	if err != nil {
		fatal("%v", err)
	}

	// Confine the read-only tools to the repo root, when we're in one, so the
	// agent can inspect surrounding source for clues.
	root := gitRoot()

	res, err := meat.Abridge(context.Background(), m, meat.Request{
		RepoRoot:    root,
		UnifiedDiff: diff,
	})
	if err != nil {
		fatal("%v", err)
	}

	if res.Summary != "" {
		fmt.Printf("# %s\n\n", res.Summary)
	}
	if strings.TrimSpace(res.SmartDiff) == "" {
		fmt.Println("(no meaningful change to read)")
	} else {
		fmt.Println(strings.TrimRight(res.SmartDiff, "\n"))
	}
	fmt.Fprintf(os.Stderr, "\nmeat: tokens in=%d out=%d\n", res.InputTokens, res.OutputTokens)
}

// readDiff returns the diff to abridge: stdin when piped, otherwise `git show`
// of the top commit (HEAD) in the current repo. The second return value names
// the source for error messages.
func readDiff() (string, string, error) {
	if stdinIsPiped() {
		data, err := readAllStdin()
		if err != nil {
			return "", "stdin", err
		}
		return string(data), "stdin", nil
	}
	// No pipe: summarize the top commit.
	out, err := git("show", "--format=fuller", "HEAD")
	if err != nil {
		return "", "HEAD", fmt.Errorf("reading HEAD (are you in a git repo?): %w", err)
	}
	return out, "HEAD", nil
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// gitRoot returns the repo root, or "" if cwd is not inside a git repo.
func gitRoot() string {
	out, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "meat: "+format+"\n", args...)
	os.Exit(1)
}
