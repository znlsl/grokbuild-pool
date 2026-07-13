package anthropic

import (
	"sort"
	"strings"

	"github.com/yshgsh1343/grokbuild2api/internal/protocol/config"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

// ModelEntry is an OpenAI-list-shaped model row used by GET /v1/models.
type ModelEntry struct {
	ID            string `json:"id"`
	Object        string `json:"object,omitempty"`
	Created       int64  `json:"created,omitempty"`
	OwnedBy       string `json:"owned_by,omitempty"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	APIBackend    string `json:"api_backend,omitempty"`
	UpstreamModel string `json:"-"`
	AliasOf       string `json:"-"`
}

// AliasModels generates Claude-facing alias entries from upstream models + config aliases.
// Claude Code discovery only accepts ids that start with "claude" or "anthropic".
func AliasModels(upstreamModels []upstream.Model, cfg config.AnthropicConfig) []ModelEntry {
	byID := make(map[string]upstream.Model, len(upstreamModels))
	for _, m := range upstreamModels {
		if strings.TrimSpace(m.ID) != "" {
			byID[m.ID] = m
		}
	}

	keys := make([]string, 0, len(cfg.ModelAliases))
	for k := range cfg.ModelAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]ModelEntry, 0, len(keys))
	seen := map[string]struct{}{}
	for _, alias := range keys {
		target := strings.TrimSpace(cfg.ModelAliases[alias])
		if target == "" {
			continue
		}
		if !isClaudeFacingID(alias) {
			// short names like "sonnet" are env defaults, not discovery ids
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		entry := ModelEntry{
			ID:            alias,
			Object:        "model",
			OwnedBy:       "anthropic",
			UpstreamModel: target,
			AliasOf:       target,
			APIBackend:    "responses",
			Name:          alias,
		}
		if um, ok := byID[target]; ok {
			entry.Created = um.Created
			if um.Name != "" {
				entry.Name = um.Name
			}
			entry.Description = um.Description
			entry.ContextWindow = um.ContextWindow
			if um.APIBackend != "" {
				entry.APIBackend = um.APIBackend
			}
		}
		out = append(out, entry)
	}
	return out
}

func isClaudeFacingID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(id, "claude") || strings.HasPrefix(id, "anthropic")
}
