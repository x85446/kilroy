package engine

import (
	"fmt"
	"regexp"
	"strings"

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
//
// Both levers are best-effort and idempotent across repeats.
func (e *Engine) applyEscalationLadder(node *model.Node, sig string, count, limit int) {
	if e == nil || node == nil {
		return
	}
	levers := make([]string, 0, 2)

	// Lever #1 — evidence injection via the dossier summary the re-run reads.
	if e.Context != nil {
		banner := fmt.Sprintf(
			"ESCALATION (deterministic failure cycle, attempt %d of %d): this exact failure has now recurred %d times and prior fixes did NOT resolve it. Do not repeat the same change. Diagnose the ROOT cause — inspect the upstream inputs, configuration, dependencies, and build settings that feed this stage, not just the surface error.\n\n",
			count, limit, count,
		)
		prev := e.Context.GetString(failureDossierContextSummaryKey, "")
		if !strings.HasPrefix(prev, "ESCALATION (deterministic failure cycle") {
			e.Context.Set(failureDossierContextSummaryKey, banner+prev)
		}
		levers = append(levers, "evidence")
	}

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

	e.appendProgress(map[string]any{
		"event":           "deterministic_failure_cycle_ladder",
		"node_id":         node.ID,
		"signature":       sig,
		"signature_count": count,
		"signature_limit": limit,
		"levers":          levers,
		"alt_provider":    altProvider,
		"alt_model":       altModel,
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
