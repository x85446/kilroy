package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const (
	failureClassTransientInfra       = "transient_infra"
	failureClassDeterministic        = "deterministic"
	failureClassCanceled             = "canceled"
	failureClassBudgetExhausted      = "budget_exhausted"
	failureClassCompilationLoop      = "compilation_loop"
	failureClassStructural           = "structural"
	defaultLoopRestartSignatureLimit = 3
	// 0 disables visit-count cycle breaking unless max_node_visits is explicitly set.
	defaultMaxNodeVisits = 0
)

var (
	failureSignatureWhitespaceRE = regexp.MustCompile(`\s+`)
	failureSignatureHexRE        = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)
	failureSignatureDigitsRE     = regexp.MustCompile(`\b\d+\b`)
	failureSignatureCommaSpaceRE = regexp.MustCompile(`,\s+`)
	transientInfraReasonHints    = []string{
		"timeout",
		"timed out",
		"context deadline exceeded",
		"connection refused",
		"connection reset",
		"could not resolve host",
		"could not resolve hostname",
		"temporary failure in name resolution",
		"network is unreachable",
		"net::err_internet_disconnected",
		"broken pipe",
		"tls handshake timeout",
		"i/o timeout",
		"no route to host",
		"temporary failure",
		"temporarily unavailable",
		"try again",
		"rate limit",
		"too many requests",
		"service unavailable",
		"gateway timeout",
		"econnrefused",
		"econnreset",
		"dial tcp",
		"transport is closing",
		"stream disconnected",
		"stream closed before",
		"stream closed",
		"connection closed",
		"unexpected eof",
		"socket hang up",
		"premature close",
		"overloaded",
		"internal server error",
		"529",
		"index.crates.io",
		"download of config.json failed",
		"toolchain_or_dependency_registry_unavailable",
		"toolchain dependency resolution blocked by network",
		"toolchain_workspace_io",
		"cross-device link",
		"invalid cross-device link",
		"os error 18",
		"502",
		"503",
		"504",
	}
	budgetExhaustedReasonHints = []string{
		"turn limit",
		"max_turns",
		"max turns",
		"token limit reached",
		"token limit exceeded",
		"max tokens",
		"max_tokens",
		"context length exceeded",
		"context window exceeded",
		"budget exhausted",
	}
	structuralReasonHints = []string{
		"write_scope_violation",
		"write scope violation",
		"scope violation",
	}
)

func isFailureLoopRestartOutcome(out runtime.Outcome) bool {
	return out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry
}

func classifyFailureClass(out runtime.Outcome) string {
	if !isFailureLoopRestartOutcome(out) {
		return ""
	}
	if hinted := normalizedFailureClass(readFailureClassHint(out)); hinted != "" {
		return hinted
	}

	reason := strings.ToLower(strings.TrimSpace(out.FailureReason))
	if reason == "" {
		return failureClassDeterministic
	}
	if strings.Contains(reason, "canceled") || strings.Contains(reason, "cancelled") {
		return failureClassCanceled
	}
	for _, hint := range transientInfraReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassTransientInfra
		}
	}
	for _, hint := range budgetExhaustedReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassBudgetExhausted
		}
	}
	for _, hint := range structuralReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassStructural
		}
	}
	return failureClassDeterministic
}

func readFailureClassHint(out runtime.Outcome) string {
	if out.Meta != nil {
		if raw, ok := out.Meta["failure_class"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	if out.ContextUpdates != nil {
		if raw, ok := out.ContextUpdates["failure_class"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func readFailureSignatureHint(out runtime.Outcome) string {
	if out.Meta != nil {
		if raw, ok := out.Meta["failure_signature"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	if out.ContextUpdates != nil {
		if raw, ok := out.ContextUpdates["failure_signature"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func normalizedFailureClass(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "<nil>":
		return ""
	case "transient", "transient_infra", "transient-infra", "infra_transient", "transient infra", "infrastructure_transient", "retryable", "toolchain_workspace_io", "toolchain-workspace-io", "toolchain_or_dependency_registry_unavailable", "toolchain-dependency-registry-unavailable":
		return failureClassTransientInfra
	case "canceled", "cancelled":
		return failureClassCanceled
	case "deterministic", "non_transient", "non-transient", "permanent", "logic", "product":
		return failureClassDeterministic
	case "budget_exhausted", "budget-exhausted", "budget exhausted", "budget":
		return failureClassBudgetExhausted
	case "compilation_loop", "compilation-loop", "compilation loop", "compile_loop", "compile-loop":
		return failureClassCompilationLoop
	case "structural", "structure", "scope_violation", "write_scope_violation":
		return failureClassStructural
	default:
		return failureClassDeterministic
	}
}

func normalizedFailureClassOrDefault(raw string) string {
	if cls := normalizedFailureClass(raw); cls != "" {
		return cls
	}
	return failureClassDeterministic
}

// isSignatureTrackedFailureClass returns true if the failure class should be
// tracked by the deterministic failure cycle breaker. Structural failures are
// included so they accumulate signatures in the main loop (in subgraphs they
// are caught earlier by the immediate structural abort).
func isSignatureTrackedFailureClass(failureClass string) bool {
	cls := normalizedFailureClassOrDefault(failureClass)
	return cls == failureClassDeterministic || cls == failureClassStructural
}

func loopRestartSignatureLimit(g *model.Graph) int {
	if g == nil {
		return defaultLoopRestartSignatureLimit
	}
	limit := parseInt(g.Attrs["loop_restart_signature_limit"], defaultLoopRestartSignatureLimit)
	if limit < 1 {
		return defaultLoopRestartSignatureLimit
	}
	return limit
}

// escalationRoute is the alternate (provider, model) the deterministic-failure
// escalation ladder assigns to a stuck node so its next attempt runs on a
// different engine.
type escalationRoute struct {
	Provider string
	Model    string
}

// loopRestartLadderStart returns the signature count at which the escalation
// ladder begins (graph attr loop_restart_ladder_start). 0 (the default) keeps
// the plain breaker behaviour: every recurrence just counts toward the limit
// with no escalation. When set, recurrences in [ladder_start, limit) fire the
// domain-agnostic escalation levers before the limit aborts the run.
func loopRestartLadderStart(g *model.Graph) int {
	if g == nil {
		return 0
	}
	start := parseInt(g.Attrs["loop_restart_ladder_start"], 0)
	if start < 0 {
		return 0
	}
	return start
}

// escalationAltRoute returns the alternate (provider, model) a stuck node is
// escalated to once the ladder engages, and whether one is configured. It is
// deliberately domain-agnostic: both values come from graph attrs
// (escalation_alt_provider / escalation_alt_model) — kilroy hardcodes no engine
// or model. The engine lever is skipped (ok=false) unless BOTH are set, so a
// provider can never be flipped without a model it can actually serve.
func escalationAltRoute(g *model.Graph) (string, string, bool) {
	if g == nil {
		return "", "", false
	}
	ap := normalizeProviderKey(strings.TrimSpace(g.Attrs["escalation_alt_provider"]))
	am := strings.TrimSpace(g.Attrs["escalation_alt_model"])
	if ap == "" || am == "" {
		return "", "", false
	}
	return ap, am, true
}

// escalationDiagnosisEnabled reports whether lever #3 (root-cause diagnosis)
// runs when the ladder engages. Default true: when an operator turns the ladder
// on (loop_restart_ladder_start>0), the diagnosis pass is the point of it. Set
// graph attr escalation_diagnosis=false/0/off/no to disable it (e.g. to save the
// extra LLM call) while keeping the evidence + engine levers.
func escalationDiagnosisEnabled(g *model.Graph) bool {
	if g == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(g.Attrs["escalation_diagnosis"])) {
	case "false", "0", "off", "no", "disable", "disabled":
		return false
	default:
		return true
	}
}

// escalationDiagnosticRoute returns the (provider, model) the lever #3 diagnosis
// agent runs on, and whether one is available. Preference: an explicit
// escalation_diagnostic_provider/model pair, else the lever #2 alternate engine
// (escalation_alt_provider/model). Like the alt route it is fully
// graph-configured — kilroy hardcodes no engine. ok=false means lever #3 has no
// engine to run on and is skipped.
func escalationDiagnosticRoute(g *model.Graph) (string, string, bool) {
	if g == nil {
		return "", "", false
	}
	dp := normalizeProviderKey(strings.TrimSpace(g.Attrs["escalation_diagnostic_provider"]))
	dm := strings.TrimSpace(g.Attrs["escalation_diagnostic_model"])
	if dp != "" && dm != "" {
		return dp, dm, true
	}
	return escalationAltRoute(g)
}

// escalationDiagnosisTimeout bounds how long the lever #3 diagnosis agent may
// run before it is abandoned (best-effort; the run continues regardless). Graph
// attr escalation_diagnosis_timeout_sec overrides the default.
func escalationDiagnosisTimeout(g *model.Graph) time.Duration {
	const def = 300
	secs := def
	if g != nil {
		secs = parseInt(g.Attrs["escalation_diagnosis_timeout_sec"], def)
		if secs < 30 {
			secs = def
		}
	}
	return time.Duration(secs) * time.Second
}

// escalatedRouteFor returns the alternate (provider, model) the ladder assigned
// to a node, if any. AgentRouter calls this before resolving the node's own
// llm_provider.
func (e *Engine) escalatedRouteFor(nodeID string) (string, string, bool) {
	if e == nil || e.escalatedRoutes == nil {
		return "", "", false
	}
	r, ok := e.escalatedRoutes[strings.TrimSpace(nodeID)]
	if !ok {
		return "", "", false
	}
	return r.Provider, r.Model, true
}

// injectEscalationIntoDossierFiles writes the escalation banner into the failure
// dossier file(s) the re-run agent reads (the worktree copy named in
// context.failure_dossier.path and the logs-root copy). It sets the prominent
// `escalation` field and prepends the banner to `summary`. Best-effort: missing
// or unreadable files are skipped. Idempotent within a single dossier version.
func (e *Engine) injectEscalationIntoDossierFiles(banner string) {
	if e == nil || e.Context == nil {
		return
	}
	paths := map[string]bool{}
	if lp := strings.TrimSpace(e.Context.GetString(failureDossierContextLogsPathKey, "")); lp != "" {
		paths[lp] = true
	}
	if rel := strings.TrimSpace(e.Context.GetString(failureDossierContextPathKey, "")); rel != "" {
		if filepath.IsAbs(rel) {
			paths[rel] = true
		} else if wt := strings.TrimSpace(e.WorktreeDir); wt != "" {
			paths[filepath.Join(wt, filepath.FromSlash(rel))] = true
		}
	}
	for p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var d failureDossier
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		d.Escalation = banner
		if !strings.HasPrefix(d.Summary, "ESCALATION (deterministic failure cycle") {
			d.Summary = banner + "\n\n" + d.Summary
		}
		_ = writeJSON(p, d)
	}
}

const diagnosisDossierMarker = "ROOT-CAUSE DIAGNOSIS (escalation lever #3)"

// injectDiagnosisIntoDossierFiles writes the lever #3 root-cause diagnosis into
// the failure dossier file(s) the re-run agent reads, both as the prominent
// `diagnosis` field and prepended to `summary`. Best-effort and idempotent: a
// repeat tick overwrites the field and re-stacks at most one diagnosis block.
func (e *Engine) injectDiagnosisIntoDossierFiles(diagnosis string) {
	if e == nil || e.Context == nil {
		return
	}
	block := diagnosisDossierMarker + ":\n" + diagnosis
	paths := map[string]bool{}
	if lp := strings.TrimSpace(e.Context.GetString(failureDossierContextLogsPathKey, "")); lp != "" {
		paths[lp] = true
	}
	if rel := strings.TrimSpace(e.Context.GetString(failureDossierContextPathKey, "")); rel != "" {
		if filepath.IsAbs(rel) {
			paths[rel] = true
		} else if wt := strings.TrimSpace(e.WorktreeDir); wt != "" {
			paths[filepath.Join(wt, filepath.FromSlash(rel))] = true
		}
	}
	for p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var d failureDossier
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		d.Diagnosis = diagnosis
		if !strings.Contains(d.Summary, diagnosisDossierMarker) {
			d.Summary = block + "\n\n" + d.Summary
		}
		_ = writeJSON(p, d)
	}
}

// cachedDiagnosis returns the persisted lever #3 root-cause diagnosis for a
// failure signature, or "" if none was produced yet. The cache survives dossier
// regeneration and resumes (checkpointed as loop_failure_diagnoses).
func (e *Engine) cachedDiagnosis(sig string) string {
	if e == nil || e.diagnosisBySignature == nil {
		return ""
	}
	return strings.TrimSpace(e.diagnosisBySignature[strings.TrimSpace(sig)])
}

// storeDiagnosis records a lever #3 diagnosis for a failure signature so later
// dossier rebuilds and resumes can re-attach it without re-running the agent.
func (e *Engine) storeDiagnosis(sig, diagnosis string) {
	sig = strings.TrimSpace(sig)
	diagnosis = strings.TrimSpace(diagnosis)
	if e == nil || sig == "" || diagnosis == "" {
		return
	}
	if e.diagnosisBySignature == nil {
		e.diagnosisBySignature = map[string]string{}
	}
	e.diagnosisBySignature[sig] = diagnosis
}

// runRootCauseDiagnosis is escalation lever #3: it runs a dedicated analysis
// agent (on the configured diagnostic engine) that reads the artifacts the
// stuck stage produced/consumed, cross-references them against the recurring
// failure, and returns a concise root-cause diagnosis. It mutates nothing in the
// worktree (analysis-only prompt) and is fully best-effort — any missing
// backend/engine/error yields an empty string and the run continues. The
// returned diagnosis is handed to the next coding attempt via the dossier.
func (e *Engine) runRootCauseDiagnosis(ctx context.Context, node *model.Node, count, limit int) string {
	if e == nil || node == nil || e.AgentBackend == nil {
		return ""
	}
	prov, modelID, ok := escalationDiagnosticRoute(e.Graph)
	if !ok {
		return ""
	}

	failureReason := ""
	dossierPath := ""
	if e.Context != nil {
		failureReason = strings.TrimSpace(e.Context.GetString("failure_reason", ""))
		dossierPath = strings.TrimSpace(e.Context.GetString(failureDossierContextPathKey, ""))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "ROOT-CAUSE ANALYSIS ONLY — do NOT modify, create, or delete any file; use read/search/inspect tools only and report your findings.\n\n")
	fmt.Fprintf(&sb, "The build/test stage %q has now failed %d times in a row with the SAME failure (attempt %d of %d before this run aborts). Prior coding attempts changed things but did NOT resolve it — so the surface error is a symptom, not the cause.\n\n", node.ID, count, count, limit)
	if failureReason != "" {
		fmt.Fprintf(&sb, "Recurring failure:\n%s\n\n", failureReason)
	}
	if dossierPath != "" {
		fmt.Fprintf(&sb, "Full structured evidence (read it first): %s\n\n", dossierPath)
	}
	sb.WriteString("Your job: find the ROOT cause. Read the scripts, configs, Makefiles, and generated artifacts that this stage produces and consumes. Cross-reference what was produced against the exact error — look specifically for internal contradictions (e.g. the same path created two incompatible ways, a tool invoked but never installed, a value required upstream but never set). Trace the failing command back to the line that makes it inevitable.\n\n")
	sb.WriteString("Output ONLY your analysis as your final message, in this shape:\n- ROOT CAUSE: <the single specific contradiction or gap, with file:line references>\n- WHY RETRIES FAILED: <why the same surface fix keeps not working>\n- FIX DIRECTION: <the minimal change that resolves the contradiction>\n")

	diagNode := &model.Node{
		ID: node.ID + "::diagnose",
		Attrs: map[string]string{
			"llm_provider":     prov,
			"llm_model":        modelID,
			"reasoning_effort": "high",
			"max_agent_turns":  "20",
		},
	}
	exec := &Execution{
		Graph:       e.Graph,
		Context:     e.Context,
		LogsRoot:    e.LogsRoot,
		WorktreeDir: e.WorktreeDir,
		Engine:      e,
		Artifacts:   e.Artifacts,
	}

	dctx, cancel := context.WithTimeout(ctx, escalationDiagnosisTimeout(e.Graph))
	defer cancel()
	text, _, err := e.AgentBackend.Run(dctx, exec, diagNode, sb.String())
	if err != nil {
		e.Warn(fmt.Sprintf("escalation diagnosis (node %s): %v", node.ID, err))
		return ""
	}
	return strings.TrimSpace(text)
}

// applyEscalationLadder fires the domain-agnostic escalation levers for a
// deterministic failure signature that has recurred into [ladder_start, limit).
// It never aborts — only count>=limit (handled by the caller) does.
//
//   - Lever #1 (evidence): prepend an ESCALATION banner to the failure-dossier
//     summary that re-run nodes already read, so the model is told the identical
//     failure recurred and to attack the root cause instead of repeating itself.
//   - Lever #2 (engine): record an alternate (provider, model) for the stuck
//     node so its next attempt runs on a different engine (only when an
//     alternate is configured on the graph).
//   - Lever #3 (diagnosis): run a dedicated root-cause analysis agent that reads
//     the produced artifacts against the failure and writes a diagnosis into the
//     dossier, so the next coding attempt starts from a root-cause analysis
//     instead of the raw error tail (only when a diagnostic engine is available
//     and escalation_diagnosis is not disabled).
//
// All levers are best-effort and idempotent across repeats.
func (e *Engine) applyEscalationLadder(ctx context.Context, node *model.Node, sig string, count, limit int) {
	if e == nil || node == nil {
		return
	}
	levers := make([]string, 0, 3)

	// Lever #1 — evidence injection. The re-run agent is told (by the failure-
	// dossier preamble) to read the dossier FILE as authoritative evidence, so
	// the banner must land IN THE FILE, not just the in-memory context key. We
	// write both: the file (what the agent reads) and the context key (for any
	// templates that reference it).
	banner := fmt.Sprintf(
		"ESCALATION (deterministic failure cycle, attempt %d of %d): this exact failure has now recurred %d times and prior fixes did NOT resolve it. Do not repeat the same change. Diagnose the ROOT cause — inspect the upstream inputs, configuration, dependencies, and build settings that feed this stage, not just the surface error.",
		count, limit, count,
	)
	if e.Context != nil {
		prev := e.Context.GetString(failureDossierContextSummaryKey, "")
		if !strings.HasPrefix(prev, "ESCALATION (deterministic failure cycle") {
			e.Context.Set(failureDossierContextSummaryKey, banner+"\n\n"+prev)
		}
	}
	e.injectEscalationIntoDossierFiles(banner)
	levers = append(levers, "evidence")

	// Lever #2 — engine escalation: route the stuck node to the alternate engine.
	altProvider, altModel := "", ""
	if ap, am, ok := escalationAltRoute(e.Graph); ok {
		if e.escalatedRoutes == nil {
			e.escalatedRoutes = map[string]escalationRoute{}
		}
		e.escalatedRoutes[strings.TrimSpace(node.ID)] = escalationRoute{Provider: ap, Model: am}
		altProvider, altModel = ap, am
		levers = append(levers, "engine")
	}

	// Lever #3 — root-cause diagnosis: run an analysis agent that reads the
	// produced artifacts against the recurring failure and writes its diagnosis
	// into the dossier the re-run agent reads, so the next attempt attacks the
	// cause instead of the symptom. Best-effort: skipped silently if disabled or
	// no diagnostic engine is configured. Runs after lever #2 so the diagnostic
	// route is independent of (not overridden by) the stuck node's alt route.
	// If this exact signature was already diagnosed on an earlier tick, reuse
	// the cached diagnosis instead of paying for the analysis agent again — the
	// root cause of an identical, unchanged failure does not change between
	// ticks. Only run the agent for a signature we have not diagnosed yet.
	diagnosed := false
	diagnosisReused := false
	if escalationDiagnosisEnabled(e.Graph) {
		diagnosis := e.cachedDiagnosis(sig)
		diagnosisReused = diagnosis != ""
		if diagnosis == "" {
			diagnosis = e.runRootCauseDiagnosis(ctx, node, count, limit)
		}
		if diagnosis != "" {
			e.storeDiagnosis(sig, diagnosis)
			e.injectDiagnosisIntoDossierFiles(diagnosis)
			if e.Context != nil {
				e.Context.Set(failureDossierContextDiagnosisKey, diagnosis)
			}
			levers = append(levers, "diagnosis")
			diagnosed = true
		}
	}

	e.appendProgress(map[string]any{
		"event":            "deterministic_failure_cycle_ladder",
		"node_id":          node.ID,
		"signature":        sig,
		"signature_count":  count,
		"signature_limit":  limit,
		"levers":           levers,
		"alt_provider":     altProvider,
		"alt_model":        altModel,
		"diagnosed":        diagnosed,
		"diagnosis_cached": diagnosisReused,
	})
}

func maxNodeVisits(g *model.Graph) int {
	if g == nil {
		return defaultMaxNodeVisits
	}
	limit := parseInt(g.Attrs["max_node_visits"], defaultMaxNodeVisits)
	if limit < 1 {
		return defaultMaxNodeVisits
	}
	return limit
}

func restartFailureSignature(nodeID string, out runtime.Outcome, failureClass string) string {
	if !isFailureLoopRestartOutcome(out) {
		return ""
	}
	reason := normalizeFailureReason(readFailureSignatureHint(out))
	if reason == "" {
		reason = normalizeFailureReason(out.FailureReason)
	}
	if reason == "" {
		reason = "status=" + strings.ToLower(strings.TrimSpace(string(out.Status)))
	}
	return strings.TrimSpace(nodeID) + "|" + normalizedFailureClassOrDefault(failureClass) + "|" + reason
}

// loopRestartPersistKeyNames returns the list of context keys configured to persist
// across loop_restart iterations via the loop_restart_persist_keys graph attribute.
func loopRestartPersistKeyNames(g *model.Graph) []string {
	if g == nil {
		return nil
	}
	raw := strings.TrimSpace(g.Attrs["loop_restart_persist_keys"])
	if raw == "" {
		return nil
	}
	var keys []string
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func normalizeFailureReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return ""
	}
	reason = failureSignatureHexRE.ReplaceAllString(reason, "<hex>")
	reason = failureSignatureDigitsRE.ReplaceAllString(reason, "<n>")
	reason = failureSignatureCommaSpaceRE.ReplaceAllString(reason, ",")
	reason = failureSignatureWhitespaceRE.ReplaceAllString(reason, " ")
	reason = strings.TrimSpace(reason)
	if len(reason) > 240 {
		reason = reason[:240]
	}
	return reason
}
