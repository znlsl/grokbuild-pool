// Package config holds the slim Anthropic-facing settings needed by protocol handlers.
// Full pool-proxy config lives under internal/config (M10); this package is a copy surface
// so anthropic handlers do not depend on the root proxy config tree.
package config

import "strings"

// AnthropicConfig controls Claude Code / Anthropic Messages entry behavior.
type AnthropicConfig struct {
	Enabled             bool              `yaml:"enabled"`
	ModelAliases        map[string]string `yaml:"model_aliases"`
	PassthroughPrefixes []string          `yaml:"passthrough_prefixes"`
	StripUnknownBetas   bool              `yaml:"strip_unknown_betas"`
	CountTokens         bool              `yaml:"count_tokens"`
}

// Config is a minimal root config used by ported anthropic tests (Default().Anthropic).
type Config struct {
	Anthropic AnthropicConfig `yaml:"anthropic"`
}

// Default returns protocol defaults matching grokbuild-proxy Anthropic aliases.
func Default() Config {
	return Config{
		Anthropic: AnthropicConfig{
			Enabled: true,
			ModelAliases: map[string]string{
				"claude-sonnet-4":   "grok-4.5",
				"claude-sonnet-4-0": "grok-4.5",
				"claude-sonnet-4-6": "grok-4.5",
				"claude-sonnet-5":   "grok-4.5",
				"claude-opus-4":     "grok-4.5",
				"claude-opus-4-6":   "grok-4.5",
				"claude-opus-4-7":   "grok-4.5",
				"claude-opus-4-8":   "grok-4.5",
				"claude-haiku-4":    "grok-composer-2.5-fast",
				"claude-haiku-4-5":  "grok-composer-2.5-fast",
				"sonnet":            "grok-4.5",
				"opus":              "grok-4.5",
				"haiku":             "grok-composer-2.5-fast",
			},
			PassthroughPrefixes: []string{"grok-"},
			StripUnknownBetas:   true,
			CountTokens:         false,
		},
	}
}

// ResolveModel maps an Anthropic model id using explicit aliases only.
func (c AnthropicConfig) ResolveModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	for _, p := range c.PassthroughPrefixes {
		if p != "" && len(model) >= len(p) && model[:len(p)] == p {
			return model
		}
	}
	if alias, ok := c.ModelAliases[model]; ok && alias != "" {
		return alias
	}
	return model
}

// ResolveModel on root Config delegates to Anthropic.
func (c Config) ResolveModel(model string) string {
	return c.Anthropic.ResolveModel(model)
}
