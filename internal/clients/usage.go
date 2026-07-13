package clients

// UsageCostFromTokens 将协议 usage 折算为额度 cost。
//
// 计量规则（P1）：
//
//	cost = max(1, (input_tokens + output_tokens) / 1000)
//
// 即每约 1000 个 token 计 1 单位额度，至少 1。
// 解析失败时调用方应 fallback cost=1（按请求 +1）。
//
// 也可用 CostFromUsageMap 直接从 JSON 的 usage 对象提取。
func UsageCostFromTokens(inputTokens, outputTokens int64) int64 {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	sum := inputTokens + outputTokens
	cost := sum / 1000
	if cost < 1 {
		return 1
	}
	return cost
}

// CostFromUsageMap 从协议 usage map 解析 cost。
// 支持：
//   - Responses / Anthropic: input_tokens + output_tokens
//   - Chat Completions: prompt_tokens + completion_tokens
//   - 回退 total_tokens
//
// ok=false 表示无法解析有效用量，调用方应 fallback 为 1。
func CostFromUsageMap(m map[string]any) (cost int64, ok bool) {
	if m == nil {
		return 0, false
	}
	in, inOK := asInt64(m["input_tokens"])
	out, outOK := asInt64(m["output_tokens"])
	if !inOK {
		in, inOK = asInt64(m["prompt_tokens"])
	}
	if !outOK {
		out, outOK = asInt64(m["completion_tokens"])
	}
	if inOK || outOK {
		return UsageCostFromTokens(in, out), true
	}
	if total, tok := asInt64(m["total_tokens"]); tok && total > 0 {
		return UsageCostFromTokens(total, 0), true
	}
	return 0, false
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case jsonNumber:
		i, err := n.Int64()
		if err != nil {
			f, err2 := n.Float64()
			if err2 != nil {
				return 0, false
			}
			return int64(f), true
		}
		return i, true
	default:
		return 0, false
	}
}

// jsonNumber 与 encoding/json.Number 兼容的最小接口。
type jsonNumber interface {
	Int64() (int64, error)
	Float64() (float64, error)
}
