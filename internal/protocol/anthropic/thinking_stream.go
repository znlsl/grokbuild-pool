package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (t *StreamTranslator) startThinkingPart(key string) error {
	if !t.Thinking.Enabled {
		return nil
	}
	if t.thinkingBlock != nil && t.thinkingStopPending {
		if err := t.finalizeThinkingBlock(false); err != nil {
			return err
		}
	}
	t.currentReasoningKey = normalizeReasoningKey(key)
	_, err := t.ensureThinkingBlock()
	return err
}

func (t *StreamTranslator) ensureThinkingBlock() (*streamBlock, error) {
	if !t.Thinking.Enabled {
		return nil, nil
	}
	if t.thinkingBlock != nil {
		return t.thinkingBlock, nil
	}
	if err := t.EnsureStart(t.State.MsgID, t.State.Model); err != nil {
		return nil, err
	}
	block := &streamBlock{Index: t.nextIndex, Kind: "thinking"}
	t.nextIndex++
	if err := t.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": block.Index,
		"content_block": map[string]any{
			"type":     "thinking",
			"thinking": "",
		},
	}); err != nil {
		return nil, err
	}
	t.thinkingBlock = block
	t.thinkingStopPending = false
	return block, nil
}

func (t *StreamTranslator) thinkingDelta(text string) error {
	if !t.Thinking.Enabled || t.Thinking.Display == "omitted" || text == "" {
		return nil
	}
	block, err := t.ensureThinkingBlock()
	if err != nil || block == nil {
		return err
	}
	if err := t.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": block.Index,
		"delta": map[string]any{
			"type":     "thinking_delta",
			"thinking": text,
		},
	}); err != nil {
		return err
	}
	block.Emitted = true
	return nil
}

func (t *StreamTranslator) captureThinkingSignature(key, signature string) {
	if !t.Thinking.Enabled || signature == "" {
		return
	}
	if key = strings.TrimSpace(key); key != "" {
		t.currentReasoningKey = normalizeReasoningKey(key)
	}
	t.thinkingSignature = signature
}

func (t *StreamTranslator) finalizeThinkingBlock(clearSignature bool) error {
	if !t.Thinking.Enabled {
		return nil
	}
	if t.thinkingBlock == nil {
		if clearSignature {
			t.thinkingSignature = ""
		}
		return nil
	}
	block := t.thinkingBlock
	if t.thinkingSignature != "" {
		if err := t.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": block.Index,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": t.thinkingSignature,
			},
		}); err != nil {
			return err
		}
	}
	if err := t.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": block.Index,
	}); err != nil {
		return err
	}
	if t.currentReasoningKey != "" {
		t.completedReasoning[t.currentReasoningKey] = struct{}{}
	}
	t.thinkingBlock = nil
	t.thinkingStopPending = false
	if clearSignature {
		t.thinkingSignature = ""
		t.currentReasoningKey = ""
	}
	return nil
}

func (t *StreamTranslator) finishThinkingBeforeContent() error {
	if t.thinkingBlock == nil {
		return nil
	}
	return t.finalizeThinkingBlock(true)
}

func (t *StreamTranslator) emitFinalReasoningItem(
	item map[string]json.RawMessage,
	fallbackKey string,
) error {
	if !t.Thinking.Enabled {
		return nil
	}
	key := normalizeReasoningKey(firstNonEmptyStream(
		rawString(item["id"]),
		fallbackKey,
	))
	if _, done := t.completedReasoning[key]; done {
		return nil
	}
	summary := extractReasoningSummary(item["summary"])
	if t.Thinking.Display == "omitted" {
		summary = ""
	}
	signature := rawString(item["encrypted_content"])
	if signature == "" {
		signature = t.thinkingSignature
	}
	if summary == "" && signature == "" {
		return nil
	}
	t.currentReasoningKey = key
	if signature != "" {
		t.thinkingSignature = signature
	}
	if _, err := t.ensureThinkingBlock(); err != nil {
		return err
	}
	if err := t.thinkingDelta(summary); err != nil {
		return err
	}
	return t.finalizeThinkingBlock(true)
}

func reasoningEventKey(root map[string]json.RawMessage) string {
	return normalizeReasoningKey(firstNonEmptyStream(
		rawString(root["item_id"]),
		rawString(root["output_index"]),
	))
}

func normalizeReasoningKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "reasoning:default"
	}
	if strings.HasPrefix(key, "reasoning:") {
		return key
	}
	return fmt.Sprintf("reasoning:%s", key)
}
