package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

const MaxPromptCacheKeyBytes = 512

// SanitizeResult 为清洗后的 Responses 负载与提取出的 model。
type SanitizeResult struct {
	Body  map[string]any
	Model string
	// Stream 为清洗后的有效 stream 标志。
	Stream bool
	// ConvID 为使用的 prompt_cache_key / 粘性会话 id。
	ConvID string
}

// SanitizeResponses 为 cli-chat-proxy 重写 Responses API JSON 体。
//
// 规则：
//   - 保留原生 Responses reasoning 项以支持无状态续写
//   - 将 system/developer 消息聚合到顶层 instructions
//   - response_format → text.format
//   - prompt_cache_key 为空时默认 convID
//   - reasoning.effort: "minimal" → "low"
//
// body 可为 []byte、json.RawMessage、map[string]any 或任意可 JSON 序列化值。
// 正文未设置 prompt_cache_key 时，用 convID 作为默认值。
func SanitizeResponses(body any, convID string) (*SanitizeResult, error) {
	obj, err := asObjectMap(body)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		obj = map[string]any{}
	}

	// 1) Aggregate system/developer → instructions.
	var instructionParts []string
	if existing := asString(obj["instructions"]); existing != "" {
		instructionParts = append(instructionParts, existing)
	}

	if input, ok := obj["input"]; ok {
		rewritten, extra, err := sanitizeInput(input)
		if err != nil {
			return nil, err
		}
		instructionParts = append(instructionParts, extra...)
		if rewritten == nil {
			delete(obj, "input")
		} else {
			obj["input"] = rewritten
		}
	}

	// 若存在 messages[] 也扫描（误发到 responses 的 chat 形态）。
	if messages, ok := obj["messages"]; ok {
		rewritten, extra, err := sanitizeInput(messages)
		if err != nil {
			return nil, err
		}
		instructionParts = append(instructionParts, extra...)
		if rewritten == nil {
			delete(obj, "messages")
		} else if _, hasInput := obj["input"]; !hasInput {
			obj["input"] = rewritten
			delete(obj, "messages")
		} else {
			obj["messages"] = rewritten
		}
	}

	if len(instructionParts) > 0 {
		obj["instructions"] = strings.Join(instructionParts, "\n\n")
	}

	// 2) response_format → text.format
	if rf, ok := obj["response_format"]; ok {
		textObj, _ := obj["text"].(map[string]any)
		if textObj == nil {
			textObj = map[string]any{}
		}
		if _, hasFormat := textObj["format"]; !hasFormat {
			textObj["format"] = normalizeResponseFormat(rf)
		}
		obj["text"] = textObj
		delete(obj, "response_format")
	}

	// 3) max_tokens → max_output_tokens (common chat field leakage)
	if _, has := obj["max_output_tokens"]; !has {
		if mt, ok := obj["max_tokens"]; ok {
			obj["max_output_tokens"] = mt
			delete(obj, "max_tokens")
		}
	} else {
		delete(obj, "max_tokens")
	}

	// 4) 将 effort 规范到 reasoning.effort。扁平写法为兼容别名；
	// 冲突值拒绝而非静默择一。
	if err := normalizeResponsesReasoning(obj); err != nil {
		return nil, err
	}

	// 5) prompt_cache_key 默认 = convID
	convID = strings.TrimSpace(convID)
	pck := strings.TrimSpace(asString(obj["prompt_cache_key"]))
	if pck == "" {
		pck = strings.TrimSpace(asString(obj["prompt_cache_id"]))
		if pck != "" {
			obj["prompt_cache_key"] = pck
			delete(obj, "prompt_cache_id")
		}
	}
	if pck == "" && convID != "" {
		obj["prompt_cache_key"] = convID
		pck = convID
	}
	if len(pck) > MaxPromptCacheKeyBytes {
		return nil, fmt.Errorf("openai sanitize: prompt_cache_key must be at most %d bytes", MaxPromptCacheKeyBytes)
	}

	// 6) tools[] — drop types Grok Responses rejects (e.g. Claude "namespace").
	// 未知变体会导致上游 422："unknown variant `namespace`, expected one of …"。
	if tools, ok := obj["tools"]; ok {
		filtered, keptNames := sanitizeResponsesTools(tools)
		if len(filtered) == 0 {
			delete(obj, "tools")
		} else {
			obj["tools"] = filtered
		}
		if tc, ok := obj["tool_choice"]; ok {
			obj["tool_choice"] = sanitizeToolChoiceAgainstTools(tc, keptNames, len(filtered) > 0)
		}
	} else if tc, ok := obj["tool_choice"]; ok {
		// 未定义 tools：将 tool_choice 规范为安全值。
		obj["tool_choice"] = sanitizeToolChoiceAgainstTools(tc, nil, false)
	}

	model := strings.TrimSpace(asString(obj["model"]))
	stream := asBool(obj["stream"])

	return &SanitizeResult{
		Body:   obj,
		Model:  model,
		Stream: stream,
		ConvID: pck,
	}, nil
}

// allowedResponsesToolTypes 为已观察到的 Grok Build / Responses 工具类型枚举。
// 其余类型（尤其 Anthropic/Claude 的 "namespace"）在上行前剥离。
var allowedResponsesToolTypes = map[string]struct{}{
	"function":           {},
	"web_search":         {},
	"x_search":           {},
	"image_generation":   {},
	"collections_search": {},
	"file_search":        {},
	"code_execution":     {},
	"code_interpreter":   {},
	"mcp":                {},
	"shell":              {},
}

// sanitizeResponsesTools 仅保留 Grok 接受类型的工具定义。
// 返回过滤后的 tools 与剩余函数名集合（供 tool_choice）。
func sanitizeResponsesTools(tools any) (filtered []any, functionNames map[string]struct{}) {
	functionNames = map[string]struct{}{}
	list := asAnySlice(tools)
	if len(list) == 0 {
		return nil, functionNames
	}
	filtered = make([]any, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			b, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var mm map[string]any
			if json.Unmarshal(b, &mm) != nil {
				continue
			}
			m = mm
		}
		typ := strings.ToLower(strings.TrimSpace(asString(m["type"])))
		// 常见客户端泄漏：chat 形态 {"type":"function","function":{...}}
		// or bare function without type.
		if typ == "" {
			if _, hasFn := m["function"]; hasFn || asString(m["name"]) != "" {
				typ = "function"
				m["type"] = "function"
			}
		}
		// 显式丢弃 Anthropic tool-search / namespace 分组工具。
		if typ == "namespace" || typ == "tool_search" || typ == "tool_search_tool_regex" ||
			typ == "tool_search_tool_bm25" || strings.HasPrefix(typ, "tool_search") {
			continue
		}
		if _, ok := allowedResponsesToolTypes[typ]; !ok {
			continue
		}
		if typ == "function" {
			// 将嵌套 chat 形态规范为扁平 Responses function tool。
			if fn, ok := m["function"].(map[string]any); ok {
				name := strings.TrimSpace(asString(fn["name"]))
				if name == "" {
					continue
				}
				out := map[string]any{
					"type": "function",
					"name": name,
				}
				if d := strings.TrimSpace(asString(fn["description"])); d != "" {
					out["description"] = d
				}
				if params, ok := fn["parameters"]; ok {
					out["parameters"] = params
				} else if params, ok := fn["input_schema"]; ok {
					out["parameters"] = params
				}
				m = out
			}
			name := strings.TrimSpace(asString(m["name"]))
			if name == "" {
				continue
			}
			functionNames[name] = struct{}{}
		}
		// 托管工具：仅保留 type（避免泄漏不支持字段）。
		if typ != "function" {
			m = map[string]any{"type": typ}
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return nil, functionNames
	}
	return filtered, functionNames
}

func sanitizeToolChoiceAgainstTools(tc any, functionNames map[string]struct{}, hasTools bool) any {
	if !hasTools {
		// 无可调用工具时采用安全默认。
		return "auto"
	}
	switch v := tc.(type) {
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "auto", "none", "required", "any":
			if s == "any" {
				return "required"
			}
			return s
		default:
			return "auto"
		}
	case map[string]any:
		typ := strings.ToLower(strings.TrimSpace(asString(v["type"])))
		switch typ {
		case "function":
			name := strings.TrimSpace(asString(v["name"]))
			if name == "" {
				if fn, ok := v["function"].(map[string]any); ok {
					name = strings.TrimSpace(asString(fn["name"]))
				}
			}
			if name == "" {
				return "auto"
			}
			if _, ok := functionNames[name]; !ok {
				return "auto"
			}
			return map[string]any{"type": "function", "name": name}
		case "auto", "none", "required":
			return typ
		case "any":
			return "required"
		default:
			// e.g. forced web_search / namespace — degrade
			return "auto"
		}
	default:
		return tc
	}
}

func asAnySlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		var list []any
		if json.Unmarshal(b, &list) != nil {
			return nil
		}
		return list
	}
}

// SanitizeResponsesBytes 为返回已序列化 JSON 的便捷封装。
func SanitizeResponsesBytes(raw []byte, convID string) (sanitized []byte, model string, stream bool, err error) {
	res, err := SanitizeResponses(raw, convID)
	if err != nil {
		return nil, "", false, err
	}
	out, err := json.Marshal(res.Body)
	if err != nil {
		return nil, "", false, fmt.Errorf("openai sanitize: marshal: %w", err)
	}
	return out, res.Model, res.Stream, nil
}

func sanitizeInput(input any) (rewritten any, instructions []string, err error) {
	switch v := input.(type) {
	case string:
		return v, nil, nil
	case []any:
		return sanitizeInputList(v)
	case []map[string]any:
		list := make([]any, len(v))
		for i := range v {
			list[i] = v[i]
		}
		return sanitizeInputList(list)
	default:
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return nil, nil, fmt.Errorf("openai sanitize: input: %w", mErr)
		}
		var list []any
		if uErr := json.Unmarshal(b, &list); uErr != nil {
			return v, nil, nil
		}
		return sanitizeInputList(list)
	}
}

func sanitizeInputList(items []any) (any, []string, error) {
	out := make([]any, 0, len(items))
	var instructions []string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			b, err := json.Marshal(it)
			if err != nil {
				out = append(out, it)
				continue
			}
			var mm map[string]any
			if json.Unmarshal(b, &mm) != nil {
				out = append(out, it)
				continue
			}
			m = mm
		}
		typ := strings.ToLower(strings.TrimSpace(asString(m["type"])))
		role := strings.ToLower(strings.TrimSpace(asString(m["role"])))

		// system/developer → instructions
		if role == "system" || role == "developer" || typ == "system" || typ == "developer" {
			if text := extractMessageText(m); text != "" {
				instructions = append(instructions, text)
			}
			continue
		}

		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, instructions, nil
	}
	return out, instructions, nil
}

func normalizeResponsesReasoning(obj map[string]any) error {
	rawReasoning, hasReasoning := obj["reasoning"]
	var reasoning map[string]any
	if rawReasoning != nil {
		var ok bool
		reasoning, ok = rawReasoning.(map[string]any)
		if !ok {
			return fmt.Errorf("openai sanitize: reasoning must be an object")
		}
	}

	nestedEffort := ""
	nestedSet := false
	if reasoning != nil {
		var err error
		nestedEffort, nestedSet, err = normalizeReasoningEffortValue(reasoning["effort"])
		if err != nil {
			return fmt.Errorf("openai sanitize: reasoning.effort: %w", err)
		}
	}

	flatValue, hasFlat := obj["reasoning_effort"]
	flatEffort, flatSet, err := normalizeReasoningEffortValue(flatValue)
	if err != nil {
		return fmt.Errorf("openai sanitize: reasoning_effort: %w", err)
	}
	if nestedSet && flatSet && nestedEffort != flatEffort {
		return fmt.Errorf(
			"openai sanitize: reasoning.effort %q conflicts with reasoning_effort %q",
			nestedEffort,
			flatEffort,
		)
	}

	effort := nestedEffort
	if effort == "" {
		effort = flatEffort
	}
	if effort != "" {
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		reasoning["effort"] = effort
		obj["reasoning"] = reasoning
	} else if hasReasoning && rawReasoning == nil {
		// 无扁平别名时保留显式 null 不动。
		obj["reasoning"] = nil
	}
	if hasFlat {
		delete(obj, "reasoning_effort")
	}
	return nil
}

func normalizeReasoningEffortValue(value any) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	effort, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("must be a string")
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return "", false, nil
	}
	if effort == "minimal" {
		effort = "low"
	}
	return effort, true, nil
}

func extractMessageText(m map[string]any) string {
	if s := asString(m["content"]); s != "" {
		return s
	}
	switch c := m["content"].(type) {
	case []any:
		var parts []string
		for _, p := range c {
			pm, ok := p.(map[string]any)
			if !ok {
				if s := asString(p); s != "" {
					parts = append(parts, s)
				}
				continue
			}
			if t := asString(pm["text"]); t != "" {
				parts = append(parts, t)
			} else if t := asString(pm["content"]); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	if s := asString(m["text"]); s != "" {
		return s
	}
	return ""
}

func normalizeResponseFormat(rf any) any {
	if m, ok := rf.(map[string]any); ok {
		if strings.EqualFold(asString(m["type"]), "json_schema") {
			if schema, ok := m["json_schema"].(map[string]any); ok {
				out := map[string]any{"type": "json_schema"}
				for _, key := range []string{"name", "description", "schema", "strict"} {
					if value, exists := schema[key]; exists {
						out[key] = value
					}
				}
				return out
			}
		}
		return m
	}
	if s := strings.TrimSpace(asString(rf)); s != "" {
		return map[string]any{"type": s}
	}
	return rf
}

func asObjectMap(body any) (map[string]any, error) {
	if body == nil {
		return map[string]any{}, nil
	}
	switch v := body.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out, nil
	case []byte:
		if len(v) == 0 {
			return map[string]any{}, nil
		}
		var out map[string]any
		if err := json.Unmarshal(v, &out); err != nil {
			return nil, fmt.Errorf("openai sanitize: invalid json: %w", err)
		}
		if out == nil {
			out = map[string]any{}
		}
		return out, nil
	case json.RawMessage:
		return asObjectMap([]byte(v))
	case string:
		return asObjectMap([]byte(v))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("openai sanitize: marshal body: %w", err)
		}
		return asObjectMap(b)
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case fmt.Stringer:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		s := string(b)
		if len(s) >= 2 && s[0] == '"' {
			var un string
			if json.Unmarshal(b, &un) == nil {
				return un
			}
		}
		return s
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		}
	case float64:
		return t != 0
	case json.Number:
		i, err := t.Int64()
		return err == nil && i != 0
	}
	return false
}
