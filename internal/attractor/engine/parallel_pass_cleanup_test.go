package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recordingEngine returns a minimal Engine wired up with an in-memory
// progress sink, so pruneOldParallelPasses can be exercised without a
// real pipeline run.
func recordingEngine(t *testing.T, keep int) (*Engine, *[]map[string]any, *sync.Mutex) {
	t.Helper()
	events := make([]map[string]any, 0, 8)
	mu := &sync.Mutex{}
	eng := &Engine{}
	eng.Options.KeepParallelPasses = keep
	eng.progressSink = func(ev map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		// shallow-copy so the engine's mutation can't race us later
		cp := make(map[string]any, len(ev))
		for k, v := range ev {
			cp[k] = v
		}
		events = append(events, cp)
	}
	return eng, &events, mu
}

// seedPasses creates synthetic on-disk pass directories under
// logsRoot/parallel/<nodeID>/pass<N>/01-key/worktree/ and returns the parent dir.
// Each "worktree" gets a small dummy file so dirSizeBytes returns non-zero.
func seedPasses(t *testing.T, logsRoot, nodeID string, passes []int) {
	t.Helper()
	for _, n := range passes {
		passDir := filepath.Join(logsRoot, "parallel", nodeID, "pass"+itoa(n), "01-impl_build", "worktree")
		if err := os.MkdirAll(passDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", passDir, err)
		}
		filler := filepath.Join(passDir, "filler.dat")
		if err := os.WriteFile(filler, make([]byte, 4096), 0o644); err != nil {
			t.Fatalf("write filler: %v", err)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func listPasses(t *testing.T, logsRoot, nodeID string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(logsRoot, "parallel", nodeID))
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestPruneOldParallelPasses_DefaultKeepsOnlyMostRecent: after pass2 starts
// (default keep=1), pass1 must be gone and a parallel_pass_pruned event must
// fire with bytes_reclaimed > 0.
func TestPruneOldParallelPasses_DefaultKeepsOnlyMostRecent(t *testing.T) {
	tmp := t.TempDir()
	nodeID := "implement_fanout"
	seedPasses(t, tmp, nodeID, []int{1, 2}) // both passes exist
	eng, events, mu := recordingEngine(t, 0) // 0 → default (1)

	// Simulate the prune call that would happen at the start of pass2 dispatch.
	eng.pruneOldParallelPasses(tmp, nodeID, 2)

	got := listPasses(t, tmp, nodeID)
	if len(got) != 1 || got[0] != "pass2" {
		t.Fatalf("expected only pass2 to remain, got %v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	var pruned map[string]any
	for _, ev := range *events {
		if ev["event"] == "parallel_pass_pruned" {
			pruned = ev
			break
		}
	}
	if pruned == nil {
		t.Fatalf("expected a parallel_pass_pruned event; got events: %v", *events)
	}
	if pruned["pruned_pass"] != 1 {
		t.Errorf("pruned_pass=%v, want 1", pruned["pruned_pass"])
	}
	if br, ok := pruned["bytes_reclaimed"].(int64); !ok || br <= 0 {
		t.Errorf("bytes_reclaimed=%v (type %T), want >0 int64", pruned["bytes_reclaimed"], pruned["bytes_reclaimed"])
	}
	if pruned["node_id"] != nodeID {
		t.Errorf("node_id=%v, want %q", pruned["node_id"], nodeID)
	}
}

// TestPruneOldParallelPasses_KeepTwoRetainsTwoMostRecent: with keep=2 and
// currentPass=3, only pass1 should be pruned. pass2 and pass3 remain.
func TestPruneOldParallelPasses_KeepTwoRetainsTwoMostRecent(t *testing.T) {
	tmp := t.TempDir()
	nodeID := "implement_fanout"
	seedPasses(t, tmp, nodeID, []int{1, 2, 3})
	eng, _, _ := recordingEngine(t, 2)

	eng.pruneOldParallelPasses(tmp, nodeID, 3)

	got := listPasses(t, tmp, nodeID)
	if len(got) != 2 {
		t.Fatalf("expected 2 passes to remain, got %v", got)
	}
	expect := map[string]bool{"pass2": true, "pass3": true}
	for _, name := range got {
		if !expect[name] {
			t.Errorf("unexpected pass remaining: %s", name)
		}
	}
}

// TestPruneOldParallelPasses_DisabledRetainsAll: with keep=-1, no passes
// should be pruned regardless of currentPass.
func TestPruneOldParallelPasses_DisabledRetainsAll(t *testing.T) {
	tmp := t.TempDir()
	nodeID := "implement_fanout"
	seedPasses(t, tmp, nodeID, []int{1, 2, 3})
	eng, events, mu := recordingEngine(t, -1)

	eng.pruneOldParallelPasses(tmp, nodeID, 3)

	got := listPasses(t, tmp, nodeID)
	if len(got) != 3 {
		t.Fatalf("expected all 3 passes to remain with keep=-1, got %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *events {
		if ev["event"] == "parallel_pass_pruned" {
			t.Errorf("did not expect parallel_pass_pruned with keep=-1, got %v", ev)
		}
	}
}

// TestPruneOldParallelPasses_FirstPassNoOp: when currentPass=1 and only pass1
// exists, prune must be a no-op (nothing older to remove).
func TestPruneOldParallelPasses_FirstPassNoOp(t *testing.T) {
	tmp := t.TempDir()
	nodeID := "implement_fanout"
	seedPasses(t, tmp, nodeID, []int{1})
	eng, events, mu := recordingEngine(t, 0)

	eng.pruneOldParallelPasses(tmp, nodeID, 1)

	got := listPasses(t, tmp, nodeID)
	if len(got) != 1 || got[0] != "pass1" {
		t.Fatalf("expected pass1 to remain on first invocation, got %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *events {
		if ev["event"] == "parallel_pass_pruned" {
			t.Errorf("unexpected parallel_pass_pruned event on first pass: %v", ev)
		}
	}
}

// TestPruneOldParallelPasses_MissingParallelRootNoOp: when the parallel dir
// doesn't exist yet, prune must not error and must not emit any events.
func TestPruneOldParallelPasses_MissingParallelRootNoOp(t *testing.T) {
	tmp := t.TempDir()
	eng, events, mu := recordingEngine(t, 0)

	eng.pruneOldParallelPasses(tmp, "never_dispatched_node", 1)

	mu.Lock()
	defer mu.Unlock()
	if len(*events) != 0 {
		t.Errorf("expected no events on missing dir, got %v", *events)
	}
}
