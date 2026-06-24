package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// findNodeCheckpointSHA returns the run-branch commit SHA whose message marks
// the given node's checkpoint for runID, or "" if not found.
func findNodeCheckpointSHA(t *testing.T, repo, branch, runID, node string) string {
	t.Helper()
	log := runCmdOut(t, repo, "git", "log", "--format=%H:%s", branch)
	want := "attractor(" + runID + "): " + node + " ("
	for line := range strings.SplitSeq(log, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(parts[1]), want) {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}

// TestResume_AppendsRunLog is a regression test for the bug where a resumed run
// never initialized eng.RunLog (NewRunLog is only called in run(), which resume
// bypasses via resumeFromLogsRoot -> runLoop). The symptom: run.log stays frozen
// at its pre-resume state for the entire resumed run, so node.completed (and
// every other RunLog event) is silently dropped — blinding everything that tails
// run.log for progress (operator `attractor status`, the launch `stopsafe`
// helper, and the usage-gate watcher, which only park at node.completed).
//
// The assertion: capture run.log's byte length before resume, then after resume
// confirm it grew AND that the appended bytes carry a node.completed event. Before
// the fix the appended slice is empty and this fails; after the fix the resumed
// node's completion is recorded.
func TestResume_AppendsRunLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=parallelogram, tool_command="echo hi > foo.txt"]
  start -> a
  a -> exit [condition="outcome=success"]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := runForTest(t, ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	rlPath := filepath.Join(res.LogsRoot, "run.log")
	before, err := os.ReadFile(rlPath)
	if err != nil {
		t.Fatalf("read run.log after initial run: %v", err)
	}
	if len(before) == 0 {
		t.Fatalf("initial run wrote an empty run.log")
	}

	// Rewind the checkpoint to just after "start" so the resume re-runs node "a"
	// (a tool node that emits node.completed).
	startSHA := findNodeCheckpointSHA(t, repo, res.RunBranch, res.RunID, "start")
	if startSHA == "" {
		t.Fatalf("could not find checkpoint commit for node start in branch %s", res.RunBranch)
	}
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "start"
	cp.CompletedNodes = []string{"start"}
	cp.GitCommitSHA = startSHA
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	if res2.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("resume final status: got %q want %q", res2.FinalStatus, runtime.FinalSuccess)
	}

	after, err := os.ReadFile(rlPath)
	if err != nil {
		t.Fatalf("read run.log after resume: %v", err)
	}
	if len(after) <= len(before) {
		t.Fatalf("run.log was not appended to during resume: before=%d after=%d bytes (eng.RunLog likely nil on the resume path)", len(before), len(after))
	}
	appended := string(after[len(before):])
	if !strings.Contains(appended, `"event":"node.completed"`) {
		t.Fatalf("resume did not record a node.completed event in run.log; appended bytes:\n%s", appended)
	}
}
