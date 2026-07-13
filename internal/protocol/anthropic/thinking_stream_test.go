package anthropic

import (
	"bytes"
	"strings"
	"testing"
)

func TestStreamThinkingRetainsSignatureAcrossSummaryParts(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_multi","model":"grok-4.5"}}`,
		``,
		`data: {"type":"response.output_item.added","item":{"id":"rs_multi","type":"reasoning","encrypted_content":"enc_multi"}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_multi"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_multi","delta":"First."}`,
		``,
		`data: {"type":"response.reasoning_summary_part.done","item_id":"rs_multi"}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_multi"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_multi","delta":"Second."}`,
		``,
		`data: {"type":"response.reasoning_summary_part.done","item_id":"rs_multi"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"id":"rs_multi","type":"reasoning"}}`,
		``,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		``,
	}, "\n")
	var out bytes.Buffer
	translator := NewStreamTranslator(
		&out,
		nil,
		"claude-opus-4-6",
		ThinkingBridgeOptions{Enabled: true, Display: "summarized"},
	)
	if err := PipeResponsesSSE(strings.NewReader(sse), translator); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if got := strings.Count(body, `"type":"thinking_delta"`); got != 2 {
		t.Fatalf("thinking deltas=%d output=%s", got, body)
	}
	if got := strings.Count(body, `"signature":"enc_multi","type":"signature_delta"`); got != 2 {
		t.Fatalf("signature deltas=%d output=%s", got, body)
	}
}

func TestStreamThinkingOmitsEmptySignature(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_no_sig","model":"grok-4.5"}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_no_sig"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_no_sig","delta":"Summary only."}`,
		``,
		`data: {"type":"response.reasoning_summary_part.done","item_id":"rs_no_sig"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"id":"rs_no_sig","type":"reasoning"}}`,
		``,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		``,
	}, "\n")
	var out bytes.Buffer
	translator := NewStreamTranslator(
		&out,
		nil,
		"claude-opus-4-6",
		ThinkingBridgeOptions{Enabled: true, Display: "summarized"},
	)
	if err := PipeResponsesSSE(strings.NewReader(sse), translator); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"thinking":"Summary only.","type":"thinking_delta"`) {
		t.Fatalf("missing summary: %s", body)
	}
	if strings.Contains(body, `"type":"signature_delta"`) {
		t.Fatalf("empty signature must not be emitted: %s", body)
	}
}
