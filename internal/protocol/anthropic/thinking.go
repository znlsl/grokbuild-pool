package anthropic

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type responsesReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type responsesTextConfig struct {
	Format responsesJSONSchemaFormat `json:"format"`
}

type responsesJSONSchemaFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// ThinkingBridgeOptions controls Responses reasoning → Anthropic thinking
// block conversion for one request.
type ThinkingBridgeOptions struct {
	Enabled bool
	Display string
}

type anthropicThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string          `json:"effort"`
	Format json.RawMessage `json:"format"`
}

// translateThinkingConfig maps Anthropic's adaptive/manual thinking controls to
// the coarse Responses reasoning levels supported by Grok. It intentionally
// does not synthesize Anthropic thinking blocks or signatures in responses.
func translateThinkingConfig(
	thinkingRaw, outputRaw json.RawMessage,
	model string,
	stripUnknown bool,
	maxTokens int,
	hasTools bool,
) (*responsesReasoning, error) {
	thinking, err := parseThinkingConfig(thinkingRaw, stripUnknown)
	if err != nil {
		return nil, err
	}
	output, err := parseOutputConfig(outputRaw, stripUnknown)
	if err != nil {
		return nil, err
	}

	requestedEffort := ""
	if output.Effort != "" {
		requestedEffort, err = normalizeAnthropicEffort(output.Effort)
		if err != nil {
			return nil, err
		}
	}

	requestedEffort, err = resolveThinkingEffort(
		thinking,
		requestedEffort,
		model,
		maxTokens,
		hasTools,
	)
	if err != nil || requestedEffort == "" {
		return nil, err
	}
	if isNonReasoningModel(model) {
		return nil, fmt.Errorf(
			"anthropic: thinking is not supported by resolved upstream model %q",
			model,
		)
	}
	reasoning := &responsesReasoning{
		Effort: grokReasoningEffort(model, requestedEffort),
	}
	if thinking.Type == "adaptive" || thinking.Type == "enabled" {
		if thinking.Display != "omitted" {
			reasoning.Summary = "auto"
		}
	}
	return reasoning, nil
}

func parseThinkingConfig(raw json.RawMessage, stripUnknown bool) (anthropicThinkingConfig, error) {
	var thinking anthropicThinkingConfig
	if !hasJSONValue(raw) {
		return thinking, nil
	}
	if err := json.Unmarshal(raw, &thinking); err != nil {
		return thinking, fmt.Errorf("anthropic: invalid thinking config: %w", err)
	}
	if !stripUnknown {
		if err := rejectUnknownConfigFields(
			raw,
			"thinking",
			"type",
			"budget_tokens",
			"display",
		); err != nil {
			return thinking, err
		}
	}
	thinking.Type = strings.ToLower(strings.TrimSpace(thinking.Type))
	thinking.Display = strings.ToLower(strings.TrimSpace(thinking.Display))
	if thinking.Type == "" {
		return thinking, fmt.Errorf("anthropic: thinking.type is required")
	}
	if thinking.Display != "" && thinking.Display != "summarized" && thinking.Display != "omitted" {
		return thinking, fmt.Errorf("anthropic: unsupported thinking.display %q", thinking.Display)
	}
	return thinking, nil
}

func thinkingBridgeFromRaw(raw json.RawMessage) ThinkingBridgeOptions {
	if !hasJSONValue(raw) {
		return ThinkingBridgeOptions{}
	}
	var thinking anthropicThinkingConfig
	if json.Unmarshal(raw, &thinking) != nil {
		return ThinkingBridgeOptions{}
	}
	switch strings.ToLower(strings.TrimSpace(thinking.Type)) {
	case "adaptive", "enabled":
		display := strings.ToLower(strings.TrimSpace(thinking.Display))
		if display == "" {
			display = "summarized"
		}
		return ThinkingBridgeOptions{Enabled: true, Display: display}
	default:
		return ThinkingBridgeOptions{}
	}
}

func parseOutputConfig(raw json.RawMessage, stripUnknown bool) (anthropicOutputConfig, error) {
	var output anthropicOutputConfig
	if !hasJSONValue(raw) {
		return output, nil
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, fmt.Errorf("anthropic: invalid output_config: %w", err)
	}
	if !stripUnknown {
		if err := rejectUnknownConfigFields(raw, "output_config", "effort", "format"); err != nil {
			return output, err
		}
	}
	output.Effort = strings.ToLower(strings.TrimSpace(output.Effort))
	return output, nil
}

func translateOutputFormat(raw json.RawMessage) (*responsesTextConfig, error) {
	if !hasJSONValue(raw) {
		return nil, nil
	}
	var output anthropicOutputConfig
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, fmt.Errorf("anthropic: invalid output_config: %w", err)
	}
	if !hasJSONValue(output.Format) {
		return nil, nil
	}
	var format struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(output.Format, &format); err != nil {
		return nil, fmt.Errorf("anthropic: invalid output_config.format: %w", err)
	}
	format.Type = strings.ToLower(strings.TrimSpace(format.Type))
	if format.Type != "json_schema" {
		return nil, fmt.Errorf(
			"anthropic: unsupported output_config.format.type %q",
			format.Type,
		)
	}
	var schema map[string]any
	if !hasJSONValue(format.Schema) || json.Unmarshal(format.Schema, &schema) != nil || schema == nil {
		return nil, fmt.Errorf("anthropic: output_config.format.schema must be a JSON object")
	}
	return &responsesTextConfig{
		Format: responsesJSONSchemaFormat{
			Type:   "json_schema",
			Name:   "anthropic_output",
			Schema: format.Schema,
			Strict: true,
		},
	}, nil
}

func resolveThinkingEffort(
	thinking anthropicThinkingConfig,
	requestedEffort string,
	model string,
	maxTokens int,
	hasTools bool,
) (string, error) {
	switch thinking.Type {
	case "":
		// Some always-thinking models and gateways send output_config.effort
		// without an explicit thinking object.
		return requestedEffort, nil
	case "adaptive":
		if thinking.BudgetTokens != nil {
			return "", fmt.Errorf("anthropic: thinking.budget_tokens is invalid with type adaptive")
		}
		if requestedEffort == "" {
			requestedEffort = "high"
		}
		return requestedEffort, nil
	case "enabled":
		if thinking.BudgetTokens == nil || *thinking.BudgetTokens < 1_024 {
			return "", fmt.Errorf("anthropic: thinking.budget_tokens must be >= 1024 with type enabled")
		}
		if !hasTools && maxTokens > 0 && *thinking.BudgetTokens >= maxTokens {
			return "", fmt.Errorf(
				"anthropic: thinking.budget_tokens must be less than max_tokens without tools",
			)
		}
		if requestedEffort == "" {
			requestedEffort = effortFromThinkingBudget(*thinking.BudgetTokens)
		}
		return requestedEffort, nil
	case "disabled":
		return disabledReasoningEffort(thinking, requestedEffort, model)
	default:
		return "", fmt.Errorf("anthropic: unsupported thinking.type %q", thinking.Type)
	}
}

func disabledReasoningEffort(
	thinking anthropicThinkingConfig,
	requestedEffort string,
	model string,
) (string, error) {
	if thinking.BudgetTokens != nil {
		return "", fmt.Errorf("anthropic: thinking.budget_tokens is invalid with type disabled")
	}
	if thinking.Display != "" {
		return "", fmt.Errorf("anthropic: thinking.display is invalid with type disabled")
	}
	if requestedEffort != "" {
		if isNonReasoningModel(model) {
			return "", nil
		}
		// Anthropic effort also controls text/tool output independently of
		// extended thinking. Grok exposes only one effort knob, so preserve the
		// requested level as the closest compatibility mapping.
		return requestedEffort, nil
	}
	if isNonReasoningModel(model) {
		return "", nil
	}
	// grok-4.3 supports disabling reasoning. grok-4.5 cannot disable it, so low
	// is the closest execution mode.
	if supportsDisabledReasoning(model) {
		return "none", nil
	}
	return "low", nil
}

func hasJSONValue(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null"
}

func rejectUnknownConfigFields(raw json.RawMessage, configName string, allowed ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("anthropic: invalid %s: %w", configName, err)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	var unknown []string
	for field := range fields {
		if _, ok := allowedSet[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf(
		"anthropic: unsupported %s field(s): %s",
		configName,
		strings.Join(unknown, ", "),
	)
}

func normalizeAnthropicEffort(effort string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort)), nil
	case "xhigh", "max":
		return "xhigh", nil
	default:
		return "", fmt.Errorf("anthropic: unsupported output_config.effort %q", effort)
	}
}

func effortFromThinkingBudget(budget int) string {
	switch {
	case budget < 4_000:
		return "low"
	case budget < 16_000:
		return "medium"
	default:
		return "high"
	}
}

func grokReasoningEffort(model, effort string) string {
	if effort == "xhigh" {
		if strings.Contains(strings.ToLower(model), "multi-agent") {
			return "xhigh"
		}
		// grok-4.5 exposes low/medium/high only.
		return "high"
	}
	return effort
}

func isNonReasoningModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "grok-composer-") ||
		strings.Contains(model, "non-reasoning")
}

func supportsDisabledReasoning(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "grok-4.3" || strings.HasPrefix(model, "grok-4.3-")
}
