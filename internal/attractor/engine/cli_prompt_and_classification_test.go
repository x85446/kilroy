package engine

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIPromptMode covers the argv-vs-stdin decision that prevents the
// "argument list too long" (E2BIG) fork/exec failure: anthropic/google use an
// argv prompt until it exceeds the per-arg kernel cap, then fall back to stdin;
// every other provider always uses stdin.
func TestCLIPromptMode(t *testing.T) {
	cases := []struct {
		provider string
		bytes    int
		want     string
	}{
		{"anthropic", 10, "arg"},
		{"anthropic", maxCLIPromptArgBytes, "arg"},         // at the cap → still arg
		{"anthropic", maxCLIPromptArgBytes + 1, "stdin"},   // over the cap → stdin
		{"anthropic", 5 * 1024 * 1024, "stdin"},            // huge → stdin
		{"google", maxCLIPromptArgBytes + 1, "stdin"},      // google shares the arg path
		{"google", 100, "arg"},                             // small google → arg
		{"openai", maxCLIPromptArgBytes + 1, "stdin"},      // non-arg provider → always stdin
		{"openai", 10, "stdin"},                            // small non-arg provider → stdin
		{"cerebras", 9 * 1024 * 1024, "stdin"},             // any other provider → stdin
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_%d", c.provider, c.bytes), func(t *testing.T) {
			if got := cliPromptMode(c.provider, c.bytes); got != c.want {
				t.Fatalf("cliPromptMode(%q,%d)=%q want %q", c.provider, c.bytes, got, c.want)
			}
		})
	}
}

// TestClassifyProviderCLIError_TransientHiccups verifies provider hiccups
// (stream/connection closes, 5xx/overloaded, transport churn) classify as
// transient_infra so a flaky provider doesn't drive the deterministic-failure
// cycle breaker to a false abort — while genuinely unknown failures still fall
// through to deterministic so the breaker keeps catching real stuck states.
func TestClassifyProviderCLIError_TransientHiccups(t *testing.T) {
	transient := []string{
		"Error: Overloaded",
		"anthropic: 529 overloaded_error",
		"upstream connect error: internal server error",
		"stream closed before completion",
		"connection closed unexpectedly",
		"socket hang up",
		"read tcp: i/o timeout",
		"429 too many requests",
		"503 service unavailable",
	}
	for _, s := range transient {
		got := classifyProviderCLIError("anthropic", s, nil)
		if got.FailureClass != failureClassTransientInfra {
			t.Errorf("stderr %q: got class %q, want transient_infra (sig %q)", s, got.FailureClass, got.FailureSignature)
		}
	}

	// Genuinely unknown provider failures must stay deterministic so the cycle
	// breaker can still catch a persistent stuck provider/auth state.
	unknown := classifyProviderCLIError("anthropic", "the model produced a malformed widget descriptor", nil)
	if unknown.FailureClass != failureClassDeterministic {
		t.Fatalf("unknown failure: got class %q, want deterministic (sig %q)", unknown.FailureClass, unknown.FailureSignature)
	}
	if !strings.Contains(unknown.FailureSignature, "provider_failure|anthropic|unknown") {
		t.Fatalf("unknown failure signature = %q, want provider_failure|anthropic|unknown", unknown.FailureSignature)
	}
}
