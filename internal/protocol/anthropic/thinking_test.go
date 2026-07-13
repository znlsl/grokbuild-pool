package anthropic

import (
	"encoding/json"
	"testing"
)

func TestTranslateRequestThinkingEffort(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		config     string
		wantEffort string
	}{
		{
			name:       "output effort without thinking",
			model:      "grok-4.5",
			config:     `"output_config":{"effort":"low"}`,
			wantEffort: "low",
		},
		{
			name:       "adaptive medium",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}`,
			wantEffort: "medium",
		},
		{
			name:       "xhigh clamps for grok 4.5",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}`,
			wantEffort: "high",
		},
		{
			name:       "max preserves xhigh for multi agent",
			model:      "grok-4.20-multi-agent",
			config:     `"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`,
			wantEffort: "xhigh",
		},
		{
			name:       "manual small budget",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"enabled","budget_tokens":3999}`,
			wantEffort: "low",
		},
		{
			name:       "manual medium budget",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"enabled","budget_tokens":4000}`,
			wantEffort: "medium",
		},
		{
			name:       "manual large budget",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"enabled","budget_tokens":16000}`,
			wantEffort: "high",
		},
		{
			name:       "disabled degrades to low on grok 4.5",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"disabled"}`,
			wantEffort: "low",
		},
		{
			name:       "disabled preserves independent output effort",
			model:      "grok-4.5",
			config:     `"thinking":{"type":"disabled"},"output_config":{"effort":"medium"}`,
			wantEffort: "medium",
		},
		{
			name:       "disabled maps to none on grok 4.3",
			model:      "grok-4.3",
			config:     `"thinking":{"type":"disabled"}`,
			wantEffort: "none",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := thinkingRequest(tc.config)
			body, _, _, err := TranslateRequest(raw, TranslateReqOptions{
				ResolvedModel:     tc.model,
				StripUnknownBetas: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Reasoning *responsesReasoning `json:"reasoning"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out.Reasoning == nil || out.Reasoning.Effort != tc.wantEffort {
				t.Fatalf(
					"reasoning=%+v want effort %q; body=%s",
					out.Reasoning,
					tc.wantEffort,
					body,
				)
			}
		})
	}
}

func TestTranslateRequestThinkingValidation(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		config string
		strict bool
	}{
		{
			name:   "unknown type",
			model:  "grok-4.5",
			config: `"thinking":{"type":"magic"}`,
		},
		{
			name:   "manual budget missing",
			model:  "grok-4.5",
			config: `"thinking":{"type":"enabled"}`,
		},
		{
			name:   "manual budget below minimum",
			model:  "grok-4.5",
			config: `"thinking":{"type":"enabled","budget_tokens":1023}`,
		},
		{
			name:   "manual budget reaches max tokens without tools",
			model:  "grok-4.5",
			config: `"thinking":{"type":"enabled","budget_tokens":32000}`,
		},
		{
			name:   "adaptive budget conflict",
			model:  "grok-4.5",
			config: `"thinking":{"type":"adaptive","budget_tokens":8000}`,
		},
		{
			name:   "invalid effort",
			model:  "grok-4.5",
			config: `"thinking":{"type":"adaptive"},"output_config":{"effort":"extreme"}`,
		},
		{
			name:   "non Anthropic minimal effort",
			model:  "grok-4.5",
			config: `"thinking":{"type":"adaptive"},"output_config":{"effort":"minimal"}`,
		},
		{
			name:   "unsupported output format type",
			model:  "grok-4.5",
			config: `"output_config":{"format":{"type":"xml","schema":{}}}`,
		},
		{
			name:   "structured output schema missing",
			model:  "grok-4.5",
			config: `"output_config":{"format":{"type":"json_schema"}}`,
		},
		{
			name:   "non reasoning upstream",
			model:  "grok-composer-2.5-fast",
			config: `"thinking":{"type":"adaptive"}`,
		},
		{
			name:   "strict unknown thinking field",
			model:  "grok-4.5",
			config: `"thinking":{"type":"adaptive","vendor_extension":true}`,
			strict: true,
		},
		{
			name:   "strict unknown output field",
			model:  "grok-4.5",
			config: `"thinking":{"type":"adaptive"},"output_config":{"effort":"high","vendor_extension":true}`,
			strict: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := TranslateRequest(thinkingRequest(tc.config), TranslateReqOptions{
				ResolvedModel:     tc.model,
				StripUnknownBetas: !tc.strict,
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestTranslateRequestStripsUnknownThinkingExtensions(t *testing.T) {
	body, _, _, err := TranslateRequest(thinkingRequest(
		`"thinking":{"type":"adaptive","vendor_extension":true},`+
			`"output_config":{"effort":"medium","vendor_extension":true}`,
	), TranslateReqOptions{
		ResolvedModel:     "grok-4.5",
		StripUnknownBetas: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reasoning *responsesReasoning `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != "medium" {
		t.Fatalf("reasoning=%+v body=%s", out.Reasoning, body)
	}
}

func TestTranslateRequestStrictModeAllowsKnownThinkingFields(t *testing.T) {
	body, _, _, err := TranslateRequest(thinkingRequest(
		`"thinking":{"type":"adaptive","display":"omitted"},`+
			`"output_config":{"effort":"high"}`,
	), TranslateReqOptions{
		ResolvedModel:     "grok-4.5",
		StripUnknownBetas: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reasoning *responsesReasoning `json:"reasoning"`
		Include   []string            `json:"include"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != "high" {
		t.Fatalf("reasoning=%+v body=%s", out.Reasoning, body)
	}
	if out.Reasoning.Summary != "" {
		t.Fatalf("omitted display requested an upstream summary: %+v", out.Reasoning)
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v body=%s", out.Include, body)
	}
}

func TestTranslateRequestSummarizedThinkingRequestsSummary(t *testing.T) {
	body, _, _, err := TranslateRequest(thinkingRequest(
		`"thinking":{"type":"adaptive","display":"summarized"},`+
			`"output_config":{"effort":"medium"}`,
	), TranslateReqOptions{
		ResolvedModel:     "grok-4.5",
		StripUnknownBetas: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reasoning *responsesReasoning `json:"reasoning"`
		Include   []string            `json:"include"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reasoning == nil ||
		out.Reasoning.Effort != "medium" ||
		out.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning=%+v body=%s", out.Reasoning, body)
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v body=%s", out.Include, body)
	}
}

func TestTranslateRequestMapsStructuredOutputFormat(t *testing.T) {
	body, _, _, err := TranslateRequest(thinkingRequest(
		`"output_config":{"effort":"high","format":{`+
			`"type":"json_schema",`+
			`"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},`+
			`"required":["ok"],"additionalProperties":false}}}`,
	), TranslateReqOptions{
		ResolvedModel:     "grok-4.5",
		StripUnknownBetas: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reasoning *responsesReasoning  `json:"reasoning"`
		Text      *responsesTextConfig `json:"text"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != "high" {
		t.Fatalf("reasoning=%+v body=%s", out.Reasoning, body)
	}
	if out.Text == nil ||
		out.Text.Format.Type != "json_schema" ||
		out.Text.Format.Name != "anthropic_output" ||
		!out.Text.Format.Strict {
		t.Fatalf("text format=%+v body=%s", out.Text, body)
	}
	var schema map[string]any
	if err := json.Unmarshal(out.Text.Format.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema=%v", schema)
	}
}

func TestTranslateRequestWithoutThinkingLeavesReasoningUnset(t *testing.T) {
	body, _, _, err := TranslateRequest(thinkingRequest(`"stream":false`), TranslateReqOptions{
		ResolvedModel:     "grok-4.5",
		StripUnknownBetas: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reasoning *responsesReasoning `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Reasoning != nil {
		t.Fatalf("unexpected reasoning override: %+v body=%s", out.Reasoning, body)
	}
}

func thinkingRequest(config string) []byte {
	return []byte(`{
		"model":"claude-opus-4-6",
		"max_tokens":32000,
		"messages":[{"role":"user","content":"solve this"}],
		` + config + `
	}`)
}
