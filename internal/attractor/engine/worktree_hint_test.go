// Tests for worktree file-not-found hint in ToolHandler error paths.

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLeadingPath(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"./scripts/check.sh", "./scripts/check.sh"},
		{"scripts/check.sh --flag", "scripts/check.sh"},
		{"bash -c 'scripts/check.sh'", "scripts/check.sh"},
		{"sh -c \"./run.sh arg1 arg2\"", "./run.sh"},
		{"echo hello", ""}, // bare command, no path
		{"ls", ""},         // bare command
		{"", ""},           // empty
		{"  ./test.sh  ", "./test.sh"},
		// Bare interpreter prefix (no -c): the script path, not the interpreter.
		{"sh scripts/validate-fmt.sh", "scripts/validate-fmt.sh"},
		{"sh scripts/validate-fmt.sh || { echo x; exit 1; }", "scripts/validate-fmt.sh"},
		{"bash scripts/y.sh --flag", "scripts/y.sh"},
		{"sh -e scripts/x.sh", "scripts/x.sh"},
		{"python3 tools/gen.py", "tools/gen.py"},
		{"node app.js", "app.js"}, // interpreter skipped → script resolved
		{"sh", ""},                // interpreter alone, nothing to resolve
	}
	for _, tt := range tests {
		got := extractLeadingPath(tt.cmd)
		if got != tt.want {
			t.Errorf("extractLeadingPath(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestWorktreeNotFoundHint_FileInRepoNotWorktree(t *testing.T) {
	repoDir := t.TempDir()
	worktreeDir := t.TempDir()

	// Create the file in repo but not in worktree.
	scriptDir := filepath.Join(repoDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "check.sh"), []byte("#!/bin/bash\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine: &Engine{
			Options: RunOptions{RepoPath: repoDir},
		},
	}
	stderr := []byte("bash: scripts/check.sh: No such file or directory")
	hint := worktreeNotFoundHint(stderr, "scripts/check.sh", execCtx)
	if hint == "" {
		t.Fatal("expected a hint, got empty string")
	}
	if want := "exists in the source repo but not in the worktree"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
	if want := "git add"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
}

func TestWorktreeNotFoundHint_FileInNeither(t *testing.T) {
	repoDir := t.TempDir()
	worktreeDir := t.TempDir()

	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine: &Engine{
			Options: RunOptions{RepoPath: repoDir},
		},
	}
	stderr := []byte("bash: scripts/missing.sh: No such file or directory")
	hint := worktreeNotFoundHint(stderr, "scripts/missing.sh", execCtx)
	if hint == "" {
		t.Fatal("expected a hint, got empty string")
	}
	if want := "not found in worktree or source repo"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
}

func TestWorktreeNotFoundHint_NoNotFoundInStderr(t *testing.T) {
	execCtx := &Execution{
		WorktreeDir: t.TempDir(),
		Engine: &Engine{
			Options: RunOptions{RepoPath: t.TempDir()},
		},
	}
	stderr := []byte("some other error")
	hint := worktreeNotFoundHint(stderr, "scripts/check.sh", execCtx)
	if hint != "" {
		t.Errorf("expected empty hint for unrelated error, got %q", hint)
	}
}

// Regression: rustc/clippy emit "... not found in `Type`" for ordinary compile
// errors. That generic "not found" must NOT be mislabeled as a missing or
// uncommitted script — this was aborting real build-failure fix loops with a
// bogus "git add && commit" hint (run-20260618T063934Z verify_fmt).
func TestWorktreeNotFoundHint_CompileErrorNotMislabeled(t *testing.T) {
	worktreeDir := t.TempDir()
	scriptDir := filepath.Join(worktreeDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "validate-fmt.sh"), []byte("#!/bin/sh\ncargo clippy\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine:      &Engine{Options: RunOptions{RepoPath: t.TempDir()}},
	}
	stderr := []byte("error[E0599]: method `mark_alive` not found in `HeartbeatTracker`\nerror: aborting due to previous error")
	cmd := "sh scripts/validate-fmt.sh || { echo 'KILROY_VALIDATE_FAILURE'; exit 1; }"
	if hint := worktreeNotFoundHint(stderr, cmd, execCtx); hint != "" {
		t.Errorf("compile error must not yield a missing-file hint, got %q", hint)
	}
}

// Even with a shell-level "not found" phrase present, if the referenced script
// exists in the worktree the failure is something else — emit no hint.
func TestWorktreeNotFoundHint_ScriptPresentNoHint(t *testing.T) {
	worktreeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreeDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine:      &Engine{Options: RunOptions{RepoPath: t.TempDir()}},
	}
	stderr := []byte("helper: not found")
	if hint := worktreeNotFoundHint(stderr, "sh scripts/check.sh", execCtx); hint != "" {
		t.Errorf("script present in worktree should yield no hint, got %q", hint)
	}
}

// fix #3: the failure reason must carry the real first error line so distinct
// underlying failures produce distinct deterministic-cycle signatures (instead
// of every non-zero exit collapsing to "exit status N" and tripping the
// cycle-breaker on a loop that is making progress).
func TestFirstActionableToolOutputLine_PicksRealError(t *testing.T) {
	out := []byte("    Checking qemu-tests v0.1.0 (/path)\n" +
		"    Checking izos-manifest v0.1.0 (/path)\n" +
		"error[E0560]: struct `SupervisorConfig` has no field named `boot_grace_period`\n")
	got := firstActionableToolOutputLine(out)
	if !strings.Contains(got, "E0560") {
		t.Errorf("expected the real error line, got %q", got)
	}
}
