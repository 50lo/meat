package meat

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// DefaultModel is meat's built-in default model.
const DefaultModel = DefaultOpenAIModel

// DefaultProvider is the built-in provider backend.
const DefaultProvider = "api"

// ResolveProvider applies the CLI provider fallback chain: the explicit value,
// then $MEAT_PROVIDER, then the built-in API backend.
func ResolveProvider(provider string) string {
	if provider == "" {
		provider = os.Getenv("MEAT_PROVIDER")
	}
	if provider == "" {
		provider = DefaultProvider
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

// ResolveModel applies the CLI model fallback chain: the explicit value, then
// $MEAT_MODEL, then DefaultModel. It performs no network or credential work, so
// callers can resolve the cache identity cheaply and offline.
func ResolveModel(model string) string {
	return resolveModel(model, DefaultModel)
}

// ResolveModelForProvider resolves model selection using the provider's
// defaults. The Codex CLI owns its configured default model, so an empty model
// remains empty for that backend rather than becoming meat's API default.
func ResolveModelForProvider(provider, model string) string {
	if ResolveProvider(provider) == "codex" {
		return resolveModel(model, "")
	}
	return ResolveModel(model)
}

// ModelCacheIdentity prevents results from different provider backends from
// colliding. "configured" represents a Codex CLI model selected in its own
// config when no explicit model was supplied.
func ModelCacheIdentity(provider, model string) string {
	provider = ResolveProvider(provider)
	model = ResolveModelForProvider(provider, model)
	if model == "" {
		model = "configured"
	}
	return provider + ":" + model
}

func resolveModel(model, fallback string) string {
	if model == "" {
		model = os.Getenv("MEAT_MODEL")
	}
	if model == "" {
		model = fallback
	}
	return model
}

// NewModelFromEnv constructs the built-in backend appropriate for model.
// Claude model IDs use Anthropic Messages; all other IDs use OpenAI Responses.
func NewModelFromEnv(ctx context.Context, model string) (Model, error) {
	return NewModelFromEnvWithProvider(ctx, "", model, "")
}

// NewModelFromEnvWithProvider constructs the selected model backend. API
// remains the default; codex delegates to the locally installed Codex CLI and
// uses its existing login/configuration instead of provider API keys.
func NewModelFromEnvWithProvider(ctx context.Context, provider, model, repoRoot string) (Model, error) {
	provider = ResolveProvider(provider)
	if provider == "codex" {
		return NewCodexFromEnv(repoRoot, model)
	}
	if provider != "api" {
		return nil, fmt.Errorf("unknown meat provider %q (want api or codex)", provider)
	}

	model = ResolveModel(model)
	if isAnthropicModel(model) {
		return NewAnthropicFromEnv(ctx, model)
	}
	return NewOpenAIFromEnv(ctx, model)
}

func isAnthropicModel(model string) bool {
	model = strings.TrimPrefix(model, "anthropic/")
	return strings.HasPrefix(model, "claude-")
}
