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

	diff, source, err := readDiff([]string{sha}, false, false)
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

// TestReadDiffRevRange verifies that `meat A..B` diffs across the range rather
// than showing a single commit.
func TestReadDiffRevRange(t *testing.T) {
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
	base, err := git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	if err := os.WriteFile("b.txt", []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-q", "-m", "second commit: add b.txt")

	for _, rng := range []string{base + "..HEAD", base + "...HEAD"} {
		diff, source, err := readDiff([]string{rng}, false, false)
		if err != nil {
			t.Fatalf("readDiff(%q): %v", rng, err)
		}
		if source != rng {
			t.Errorf("source = %q, want %q", source, rng)
		}
		// The range adds b.txt (the only change between base and HEAD).
		if !strings.Contains(diff, "b.txt") {
			t.Errorf("range %q diff missing b.txt:\n%s", rng, diff)
		}
		// `git diff` of a range carries no commit metadata; if we'd run
		// `git show` instead, the commit message would leak in.
		if strings.Contains(diff, "second commit") {
			t.Errorf("range %q produced commit metadata; used 'git show' not 'git diff':\n%s", rng, diff)
		}
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
	if _, _, err := readDiff([]string{"deadbeef"}, false, false); err == nil {
		t.Fatal("readDiff with unknown revision: want error, got nil")
	}
}

// TestReadDiffTooManyArgs rejects more than one revision.
func TestReadDiffTooManyArgs(t *testing.T) {
	if _, _, err := readDiff([]string{"a", "b"}, false, false); err == nil {
		t.Fatal("readDiff with two args: want error, got nil")
	}
}

// TestReadDiffMergeCommit: plain `git show` on a merge commit emits no diff at
// all, which used to make meat report "no diff to read". The first-parent diff
// (what merging the branch changed on main) must come through.
func TestReadDiffMergeCommit(t *testing.T) {
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
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("a.txt", "base\n")
	run("add", "a.txt")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "feature")
	write("feat.txt", "feature work\n")
	run("add", "feat.txt")
	run("commit", "-q", "-m", "feature commit")
	run("checkout", "-q", "main")
	write("main.txt", "main work\n")
	run("add", "main.txt")
	run("commit", "-q", "-m", "main commit")
	run("merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	diff, _, err := readDiff(nil, false, false) // HEAD is the merge
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "feat.txt") {
		t.Errorf("merge commit produced no first-parent diff (feat.txt missing):\n%s", diff)
	}
	if !strings.Contains(diff, "merge feature") {
		t.Errorf("merge commit metadata missing:\n%s", diff)
	}
}

// TestReadDiffStagedAndWorktree exercises -staged (index vs HEAD) and -w
// (working tree vs index) against a real repo.
func TestReadDiffStagedAndWorktree(t *testing.T) {
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
	if err := os.WriteFile("a.txt", []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "v1")

	// Stage one change, then make a further unstaged edit.
	if err := os.WriteFile("a.txt", []byte("v2 staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	if err := os.WriteFile("a.txt", []byte("v3 worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stagedDiff, _, err := readDiff(nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stagedDiff, "v2 staged") || strings.Contains(stagedDiff, "v3 worktree") {
		t.Errorf("-staged should show index-vs-HEAD only:\n%s", stagedDiff)
	}

	wtDiff, _, err := readDiff(nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wtDiff, "v3 worktree") || strings.Contains(wtDiff, "-v1") {
		t.Errorf("-w should show worktree-vs-index only:\n%s", wtDiff)
	}
}

// TestReadDiffFlagConflicts rejects nonsensical flag combinations.
func TestReadDiffFlagConflicts(t *testing.T) {
	if _, _, err := readDiff(nil, true, true); err == nil {
		t.Error("-staged with -w: want error")
	}
	if _, _, err := readDiff([]string{"HEAD"}, true, false); err == nil {
		t.Error("-staged with a revision: want error")
	}
	if _, _, err := readDiff([]string{"HEAD"}, false, true); err == nil {
		t.Error("-w with a revision: want error")
	}
}
