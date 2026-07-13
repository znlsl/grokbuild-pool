package clients

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ParseUsageCostFromBody 从非流式 JSON 响应体尽力提取 usage 并换算 cost。
// 支持顶层 usage、以及 response.usage（部分包装）。
// ok=false 时调用方应 fallback cost=1。
func ParseUsageCostFromBody(body []byte) (cost int64, ok bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return 0, false
	}
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return 0, false
	}
	if u, ok := root["usage"].(map[string]any); ok {
		return CostFromUsageMap(u)
	}
	// 部分 Responses 包装
	if resp, ok := root["response"].(map[string]any); ok {
		if u, ok := resp["usage"].(map[string]any); ok {
			return CostFromUsageMap(u)
		}
	}
	return 0, false
}

// ParseUsageCostFromSSE 从 SSE 文本末尾尽力提取 usage（response.completed 等）。
// 流式结束时调用；解析失败返回 ok=false。
func ParseUsageCostFromSSE(sseBody []byte) (cost int64, ok bool) {
	if len(sseBody) == 0 {
		return 0, false
	}
	// 限制扫描尾部，避免超大缓冲
	const maxScan = 256 << 10
	data := sseBody
	if len(data) > maxScan {
		data = data[len(data)-maxScan:]
	}
	// 从后往前找含 "usage" 的 data 行
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !bytes.Contains(payload, []byte("usage")) {
			continue
		}
		var root map[string]any
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			continue
		}
		if u, ok := root["usage"].(map[string]any); ok {
			if c, ok2 := CostFromUsageMap(u); ok2 {
				return c, true
			}
		}
		if resp, ok := root["response"].(map[string]any); ok {
			if u, ok2 := resp["usage"].(map[string]any); ok2 {
				if c, ok3 := CostFromUsageMap(u); ok3 {
					return c, true
				}
			}
		}
		// message_delta / message_start 风格
		if msg, ok := root["message"].(map[string]any); ok {
			if u, ok2 := msg["usage"].(map[string]any); ok2 {
				if c, ok3 := CostFromUsageMap(u); ok3 {
					return c, true
				}
			}
		}
	}
	// 宽松：整段 JSON 对象扫描
	s := string(data)
	if idx := strings.LastIndex(s, `"usage"`); idx >= 0 {
		// 回退到最近的 {
		start := strings.LastIndex(s[:idx], "{")
		if start >= 0 {
			// 尝试从 start 解析一个对象（可能失败，忽略）
			var root map[string]any
			dec := json.NewDecoder(strings.NewReader(s[start:]))
			dec.UseNumber()
			if err := dec.Decode(&root); err == nil {
				if u, ok := root["usage"].(map[string]any); ok {
					return CostFromUsageMap(u)
				}
			}
		}
	}
	return 0, false
}
