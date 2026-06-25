package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestReadDiffRevisionArg verifies that `meat <revision>` summarizes the named
// commit, exercising the real `git show` path against a throwaway repo.
func TestReadDiffRevisionArg(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not found: %v", err)
	}

	dir := t.TempDir()
	t.Chdir(dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile("a.txt", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first commit: add a.txt")

	sha, err := git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)

	// A second commit so HEAD != the revision we ask for; confirms the arg is
	// honored rather than defaulting to HEAD.
	if err := os.WriteFile("b.txt", []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-q", "-m", "second commit: add b.txt")

	diff, source, err := readDiff([]string{sha})
	if err != nil {
		t.Fatalf("readDiff(%q): %v", sha, err)
	}
	if source != sha {
		t.Errorf("source = %q, want %q", source, sha)
	}
	if !strings.Contains(diff, "a.txt") || !strings.Contains(diff, "first commit") {
		t.Errorf("diff does not describe the requested commit:\n%s", diff)
	}
	if strings.Contains(diff, "b.txt") {
		t.Errorf("diff leaked a later commit (b.txt); arg not honored:\n%s", diff)
	}
}

// TestReadDiffBadRevision surfaces a useful error for an unknown revision.
func TestReadDiffBadRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not found: %v", err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	cmd := exec.Command("git", "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if _, _, err := readDiff([]string{"deadbeef"}); err == nil {
		t.Fatal("readDiff with unknown revision: want error, got nil")
	}
}

// TestReadDiffTooManyArgs rejects more than one revision.
func TestReadDiffTooManyArgs(t *testing.T) {
	if _, _, err := readDiff([]string{"a", "b"}); err == nil {
		t.Fatal("readDiff with two args: want error, got nil")
	}
}
