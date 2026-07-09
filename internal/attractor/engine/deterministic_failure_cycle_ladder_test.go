package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/llm"
)

// asLadderInt coerces a JSON-decoded numeric (float64) or int back to int.
func asLadderInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return -1
}

func ladderLeversContain(v any, want string) bool {
	switch lv := v.(type) {
	case []any:
		for _, x := range lv {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range lv {
			if s == want {
				return true
			}
		}
	}
	return false
}

// TestRun_DeterministicFailureCycle_Ladder drives the MAIN-loop breaker through
// a deterministic failure cycle with the escalation ladder configured
// (ladder_start=6, limit=10). It asserts the breaker does NOT abort before
// count 10, that the ladder fires at counts 6-9 with both levers and the
// configured alternate engine, and that abort happens at count 10.
func TestRun_DeterministicFailureCycle_Ladder(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [default_max_retry=0, loop_restart_signature_limit="10", loop_restart_ladder_start="6", escalation_alt_provider="openai", escalation_alt_model="gpt-5.5"]
  start [shape=Mdiamond]
  exit [shape=Msquare]

  implement [
    shape=parallelogram,
    tool_command="echo implement_fail >> log.txt; exit 1"
  ]
  verify [
    shape=parallelogram,
    tool_command="echo verify_fail >> log.txt; exit 1"
  ]
  check [shape=diamond]

  start -> implement
  implement -> verify
  verify -> check
  check -> implement [condition="outcome=fail", label="retry"]
  check -> exit [condition="outcome=success"]
  check -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	logsRoot := t.TempDir()
	_, err := runForTest(t, ctx, dot, RunOptions{RepoPath: repo, RunID: "detcycleladder", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected deterministic failure cycle abort, got success")
	}
	if !strings.Contains(err.Error(), "deterministic failure cycle") {
		t.Fatalf("expected deterministic failure cycle error, got: %v", err)
	}

	events := readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))

	ladderCounts := map[int]bool{}
	var breakerCounts []int
	sawEvidence, sawEngine := false, false
	altProvider := ""
	for _, ev := range events {
		switch ev["event"] {
		case "deterministic_failure_cycle_ladder":
			c := asLadderInt(ev["signature_count"])
			ladderCounts[c] = true
			if ladderLeversContain(ev["levers"], "evidence") {
				sawEvidence = true
			}
			if ladderLeversContain(ev["levers"], "engine") {
				sawEngine = true
			}
			if p, ok := ev["alt_provider"].(string); ok && p != "" {
				altProvider = p
			}
		case "deterministic_failure_cycle_breaker":
			breakerCounts = append(breakerCounts, asLadderInt(ev["signature_count"]))
		}
	}

	// The breaker must never abort before the limit (10).
	if len(breakerCounts) == 0 {
		t.Fatalf("expected a deterministic_failure_cycle_breaker event, got none")
	}
	for _, c := range breakerCounts {
		if c < 10 {
			t.Fatalf("breaker aborted at count %d (<10) — must not abort before the limit; counts=%v", c, breakerCounts)
		}
	}

	// The ladder must fire at counts 6..9 (verbatim retry for 1..5, then escalate).
	for _, want := range []int{6, 7, 8, 9} {
		if !ladderCounts[want] {
			t.Fatalf("expected escalation ladder event at signature_count=%d; got ladder counts=%v", want, ladderCounts)
		}
	}
	if ladderCounts[5] {
		t.Fatalf("ladder fired at count 5 — it must only engage at/after ladder_start=6")
	}

	if !sawEvidence {
		t.Fatalf("expected ladder events to include the 'evidence' lever")
	}
	if !sawEngine {
		t.Fatalf("expected ladder events to include the 'engine' lever")
	}
	if altProvider != "openai" {
		t.Fatalf("expected ladder alt_provider=openai, got %q", altProvider)
	}
}

// TestEscalationLadder_AppliesEvidenceAndRoute unit-tests the two levers'
// effects directly: the escalation banner is prepended to the failure-dossier
// summary that re-run nodes read (lever #1), and the stuck node gets the
// configured alternate (provider, model) recorded (lever #2).
func TestEscalationLadder_AppliesEvidenceAndRoute(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="ladder unit", loop_restart_signature_limit="10", loop_restart_ladder_start="6", escalation_alt_provider="openai", escalation_alt_model="gpt-5.5", escalation_diagnosis="false"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  n [shape=diamond, type="noop"]
  start -> n
  n -> exit [condition="outcome=success"]
  n -> exit
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "ladder-unit", dot)
	node := &model.Node{ID: "n"}

	eng.applyEscalationLadder(context.Background(), node, "n|deterministic|boom", 6, 10)

	summary := eng.Context.GetString(failureDossierContextSummaryKey, "")
	if !strings.Contains(summary, "ESCALATION") {
		t.Fatalf("lever #1: expected ESCALATION banner in failure-dossier summary, got %q", summary)
	}
	p, m, ok := eng.escalatedRouteFor("n")
	if !ok || p != "openai" || m != "gpt-5.5" {
		t.Fatalf("lever #2: expected escalated route openai/gpt-5.5, got %q/%q ok=%v", p, m, ok)
	}

	// lever #1 must reach the FILE the re-run agent reads, not just the context
	// key. Seed a dossier file at the context path, re-run the ladder, assert the
	// file now carries the escalation banner.
	dossierPath := filepath.Join(logsRoot, failureDossierFileName)
	if err := writeJSON(dossierPath, failureDossier{Version: 1, FailedNodeID: "n", Summary: "original summary"}); err != nil {
		t.Fatalf("seed dossier: %v", err)
	}
	eng.Context.Set(failureDossierContextLogsPathKey, dossierPath)
	eng.applyEscalationLadder(context.Background(), node, "n|deterministic|boom", 8, 10)
	raw, err := os.ReadFile(dossierPath)
	if err != nil {
		t.Fatalf("read dossier: %v", err)
	}
	var d failureDossier
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal dossier: %v", err)
	}
	if !strings.Contains(d.Escalation, "ESCALATION") {
		t.Fatalf("lever #1: expected escalation field in dossier FILE, got %q", d.Escalation)
	}
	if !strings.HasPrefix(d.Summary, "ESCALATION") {
		t.Fatalf("lever #1: expected banner prepended to dossier FILE summary, got %q", d.Summary)
	}

	// Idempotent: a second ladder tick must not double-stack the banner.
	eng.applyEscalationLadder(context.Background(), node, "n|deterministic|boom", 7, 10)
	summary2 := eng.Context.GetString(failureDossierContextSummaryKey, "")
	if strings.Count(summary2, "ESCALATION (deterministic failure cycle") != 1 {
		t.Fatalf("expected exactly one escalation banner after two ticks, got %d", strings.Count(summary2, "ESCALATION (deterministic failure cycle"))
	}
}

// TestEscalationLadder_NoAltProvider_EvidenceOnly verifies the engine lever is
// skipped (no alternate route) when the graph does not configure one, while the
// evidence lever still fires.
func TestEscalationLadder_NoAltProvider_EvidenceOnly(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="ladder unit noalt", loop_restart_signature_limit="10", loop_restart_ladder_start="6"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  n [shape=diamond, type="noop"]
  start -> n
  n -> exit [condition="outcome=success"]
  n -> exit
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "ladder-unit-noalt", dot)
	eng.applyEscalationLadder(context.Background(), &model.Node{ID: "n"}, "n|deterministic|boom", 6, 10)

	if !strings.Contains(eng.Context.GetString(failureDossierContextSummaryKey, ""), "ESCALATION") {
		t.Fatalf("expected evidence banner even without an alternate engine")
	}
	if _, _, ok := eng.escalatedRouteFor("n"); ok {
		t.Fatalf("expected no escalated route when escalation_alt_provider/model are unset")
	}
}

// fakeDiagnosisBackend is an AgentBackend stub that captures the diagnostic
// agent's node + prompt and returns a canned root-cause diagnosis.
type fakeDiagnosisBackend struct {
	gotNodeID   string
	gotProvider string
	gotModel    string
	gotPrompt   string
	reply       string
	err         error
	calls       int
}

func (b *fakeDiagnosisBackend) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	_ = ctx
	_ = exec
	b.calls++
	b.gotNodeID = node.ID
	b.gotProvider = node.Attr("llm_provider", "")
	b.gotModel = node.Attr("llm_model", "")
	b.gotPrompt = prompt
	if b.err != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: b.err.Error()}, b.err
	}
	return b.reply, &runtime.Outcome{Status: runtime.StatusSuccess}, nil
}

// TestEscalationLadder_DiagnosisLever verifies lever #3: when the ladder fires
// and a diagnostic engine is configured, a root-cause analysis agent runs on
// that engine with an analysis-only prompt, and its diagnosis is written into
// the failure-dossier FILE the re-run agent reads (the `diagnosis` field + a
// summary block) plus the diagnosis context key, and "diagnosis" is reported in
// the ladder event's levers.
func TestEscalationLadder_DiagnosisLever(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="ladder diag", loop_restart_signature_limit="10", loop_restart_ladder_start="6", escalation_alt_provider="openai", escalation_alt_model="gpt-5.5", escalation_diagnostic_provider="anthropic", escalation_diagnostic_model="claude-opus-4-8"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  n [shape=diamond, type="noop"]
  start -> n
  n -> exit [condition="outcome=success"]
  n -> exit
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "ladder-diag", dot)
	backend := &fakeDiagnosisBackend{reply: "ROOT CAUSE: /izos is created as a symlink (line 39) then mkdir as a dir (line 60) — same path, can't be both."}
	eng.AgentBackend = backend

	// Seed the dossier file the lever writes into, and point the context keys at it.
	dossierPath := filepath.Join(logsRoot, failureDossierFileName)
	if err := writeJSON(dossierPath, failureDossier{Version: 1, FailedNodeID: "n", Summary: "original summary"}); err != nil {
		t.Fatalf("seed dossier: %v", err)
	}
	eng.Context.Set(failureDossierContextLogsPathKey, dossierPath)
	eng.Context.Set("failure_reason", "mkdir: cannot create directory '/tmp/x/izos': File exists")

	eng.applyEscalationLadder(context.Background(), &model.Node{ID: "n"}, "n|deterministic|boom", 6, 10)

	// The diagnostic agent must have run on the configured diagnostic engine,
	// on a distinct synthetic node, with an analysis-only prompt.
	if backend.gotNodeID != "n::diagnose" {
		t.Fatalf("expected diagnostic node id n::diagnose, got %q", backend.gotNodeID)
	}
	if backend.gotProvider != "anthropic" || backend.gotModel != "claude-opus-4-8" {
		t.Fatalf("expected diagnosis on anthropic/claude-opus-4-8, got %q/%q", backend.gotProvider, backend.gotModel)
	}
	if !strings.Contains(backend.gotPrompt, "ROOT-CAUSE ANALYSIS ONLY") {
		t.Fatalf("expected analysis-only diagnostic prompt, got %q", backend.gotPrompt)
	}

	// The diagnosis must land in the dossier FILE the re-run agent reads.
	raw, err := os.ReadFile(dossierPath)
	if err != nil {
		t.Fatalf("read dossier: %v", err)
	}
	var d failureDossier
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal dossier: %v", err)
	}
	if !strings.Contains(d.Diagnosis, "symlink") {
		t.Fatalf("lever #3: expected diagnosis field in dossier FILE, got %q", d.Diagnosis)
	}
	if !strings.Contains(d.Summary, diagnosisDossierMarker) {
		t.Fatalf("lever #3: expected diagnosis block prepended to dossier summary, got %q", d.Summary)
	}
	if got := eng.Context.GetString(failureDossierContextDiagnosisKey, ""); !strings.Contains(got, "symlink") {
		t.Fatalf("lever #3: expected diagnosis context key set, got %q", got)
	}
}

// TestEscalationLadder_DiagnosisPersists is the fix-c regression test. It proves
// a lever #3 diagnosis, once produced, is cached by failure signature so that:
//   (1) a later ladder tick with the SAME signature reuses the cache instead of
//       re-running the (expensive) analysis agent, and still reports the lever;
//   (2) updateFailureDossierContext — which regenerates the dossier
//       latest-failure-wins on every fail/retry — re-attaches the cached
//       diagnosis so it survives into the dossier the next coding attempt reads;
//   (3) the cache is checkpointed and restored (survives resumes).
func TestEscalationLadder_DiagnosisPersists(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="ladder diag persist", loop_restart_signature_limit="10", loop_restart_ladder_start="6", escalation_diagnostic_provider="anthropic", escalation_diagnostic_model="claude-opus-4-8"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  n [shape=diamond, type="noop"]
  start -> n
  n -> exit [condition="outcome=success"]
  n -> exit
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "ladder-diag-persist", dot)
	backend := &fakeDiagnosisBackend{reply: "ROOT CAUSE: /izos symlink-vs-dir collision."}
	eng.AgentBackend = backend

	dossierPath := filepath.Join(logsRoot, failureDossierFileName)
	if err := writeJSON(dossierPath, failureDossier{Version: 1, FailedNodeID: "n", Summary: "original summary"}); err != nil {
		t.Fatalf("seed dossier: %v", err)
	}
	eng.Context.Set(failureDossierContextLogsPathKey, dossierPath)

	node := &model.Node{ID: "n"}
	const sig = "n|deterministic|boom"

	// First tick: the analysis agent runs once and populates the cache.
	eng.applyEscalationLadder(context.Background(), node, sig, 6, 10)
	if backend.calls != 1 {
		t.Fatalf("expected diagnostic agent to run once on first tick, ran %d times", backend.calls)
	}
	if eng.cachedDiagnosis(sig) == "" {
		t.Fatalf("expected diagnosis cached under signature %q", sig)
	}

	// (1) Second tick, same signature: reuse the cache, do NOT re-run the agent.
	eng.applyEscalationLadder(context.Background(), node, sig, 7, 10)
	if backend.calls != 1 {
		t.Fatalf("expected cached diagnosis reuse on second tick (no re-run), agent ran %d times", backend.calls)
	}

	// (2) A fresh dossier rebuild (latest-failure-wins) must re-attach the cached
	// diagnosis. Overwrite the dossier file with a diagnosis-free version the way
	// updateFailureDossierContext does, then drive it and assert re-attachment.
	out := runtime.Outcome{Status: runtime.StatusFail, FailureReason: "boom"}
	eng.updateFailureDossierContext(node, out, failureClassDeterministic, map[string]int{})
	raw, err := os.ReadFile(dossierPath)
	if err != nil {
		t.Fatalf("read rebuilt dossier: %v", err)
	}
	var d failureDossier
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal rebuilt dossier: %v", err)
	}
	if !strings.Contains(d.Diagnosis, "collision") {
		t.Fatalf("fix-c: expected cached diagnosis re-attached to rebuilt dossier, got %q", d.Diagnosis)
	}
	if !strings.Contains(d.Summary, diagnosisDossierMarker) {
		t.Fatalf("fix-c: expected diagnosis block in rebuilt dossier summary, got %q", d.Summary)
	}

	// (3) Checkpoint round-trip: both the in-memory (map[string]string) and the
	// JSON-reloaded (map[string]any) forms restore the cache.
	cp := runtime.NewCheckpoint()
	cp.Extra = map[string]any{"loop_failure_diagnoses": copyStringStringMap(eng.diagnosisBySignature)}
	if got := restoreLoopFailureDiagnoses(cp)[sig]; !strings.Contains(got, "collision") {
		t.Fatalf("fix-c: expected diagnosis restored from map[string]string checkpoint, got %q", got)
	}
	cpJSON := runtime.NewCheckpoint()
	cpJSON.Extra = map[string]any{"loop_failure_diagnoses": map[string]any{sig: "ROOT CAUSE: /izos symlink-vs-dir collision."}}
	if got := restoreLoopFailureDiagnoses(cpJSON)[sig]; !strings.Contains(got, "collision") {
		t.Fatalf("fix-c: expected diagnosis restored from map[string]any checkpoint, got %q", got)
	}
}

// TestEscalationLadder_DiagnosisLever_NoEngine confirms lever #3 is skipped
// cleanly (no panic, no diagnosis) when no diagnostic engine is configured,
// while the evidence lever still fires.
func TestEscalationLadder_DiagnosisLever_NoEngine(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="ladder diag noeng", loop_restart_signature_limit="10", loop_restart_ladder_start="6"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  n [shape=diamond, type="noop"]
  start -> n
  n -> exit [condition="outcome=success"]
  n -> exit
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "ladder-diag-noeng", dot)
	backend := &fakeDiagnosisBackend{reply: "should not be used"}
	eng.AgentBackend = backend

	eng.applyEscalationLadder(context.Background(), &model.Node{ID: "n"}, "n|deterministic|boom", 6, 10)

	if backend.gotNodeID != "" {
		t.Fatalf("expected no diagnostic run without a diagnostic engine, but backend was called for %q", backend.gotNodeID)
	}
	if !strings.Contains(eng.Context.GetString(failureDossierContextSummaryKey, ""), "ESCALATION") {
		t.Fatalf("expected evidence banner to still fire when lever #3 is skipped")
	}
}

// recordingAdapter captures the model it was asked to complete.
type recordingAdapter struct {
	name        string
	gotProvider string
	gotModel    string
}

func (a *recordingAdapter) Name() string { return a.name }
func (a *recordingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.gotModel = req.Model
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}, nil
}
func (a *recordingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	a.gotModel = req.Model
	st := llm.NewChanStream(nil)
	go func() {
		defer st.CloseSend()
		resp := llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}
		finish := llm.FinishReason{Reason: "stop"}
		usage := llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish, Usage: &usage, Response: &resp})
	}()
	return st, nil
}

// TestAgentRouter_EscalationRoute_OverridesProvider proves AgentRouter.Run
// honors an engine escalation route: a node declared on anthropic is routed to
// the escalated openai/gpt-5.5 instead, and the provider_selected event reports
// the override with escalated=true.
func TestAgentRouter_EscalationRoute_OverridesProvider(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	r := NewAgentRouterWithRuntimes(cfg, nil, map[string]ProviderRuntime{
		"openai": {Key: "openai", Backend: BackendAPI},
	})
	adapter := &recordingAdapter{name: "openai"}
	r.apiClientFactory = func(map[string]ProviderRuntime) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, nil
	}

	var captured []map[string]any
	eng := &Engine{
		Context:         runtime.NewContext(),
		escalatedRoutes: map[string]escalationRoute{"stage-a": {Provider: "openai", Model: "gpt-5.5"}},
		progressSink:    func(ev map[string]any) { captured = append(captured, ev) },
	}
	execCtx := &Execution{
		LogsRoot:    t.TempDir(),
		WorktreeDir: t.TempDir(),
		Engine:      eng,
	}
	node := &model.Node{
		ID: "stage-a",
		Attrs: map[string]string{
			"llm_provider": "anthropic",
			"llm_model":    "claude-opus-4-8",
			"agent_mode":   "one_shot",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, _, err := r.Run(ctx, execCtx, node, "say hi")
	if err != nil {
		t.Fatalf("Run with escalation route: unexpected error: %v", err)
	}
	if !strings.Contains(text, "ok") {
		t.Fatalf("expected adapter reply 'ok', got %q", text)
	}
	if adapter.gotModel != "gpt-5.5" {
		t.Fatalf("expected escalated model gpt-5.5 to reach the openai adapter, got %q", adapter.gotModel)
	}

	var ps map[string]any
	for _, ev := range captured {
		if ev["event"] == "provider_selected" {
			ps = ev
		}
	}
	if ps == nil {
		t.Fatalf("expected a provider_selected event, captured=%v", captured)
	}
	if ps["provider"] != "openai" {
		t.Fatalf("expected provider_selected provider=openai (escalated from anthropic), got %v", ps["provider"])
	}
	if ps["escalated"] != true {
		t.Fatalf("expected provider_selected escalated=true, got %v", ps["escalated"])
	}
	if ps["source"] != "escalation" {
		t.Fatalf("expected provider_selected source=escalation, got %v", ps["source"])
	}
}
