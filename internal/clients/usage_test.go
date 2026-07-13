package clients

import "testing"

func TestUsageCostFromTokens(t *testing.T) {
	if c := UsageCostFromTokens(0, 0); c != 1 {
		t.Fatalf("empty=%d", c)
	}
	if c := UsageCostFromTokens(500, 400); c != 1 {
		t.Fatalf("under1k=%d", c)
	}
	if c := UsageCostFromTokens(1000, 0); c != 1 {
		t.Fatalf("exact1k=%d", c)
	}
	if c := UsageCostFromTokens(1500, 500); c != 2 {
		t.Fatalf("2k=%d", c)
	}
	if c := UsageCostFromTokens(9999, 1); c != 10 {
		t.Fatalf("10k=%d", c)
	}
}

func TestParseUsageCostFromBody(t *testing.T) {
	body := []byte(`{"id":"r1","usage":{"input_tokens":2000,"output_tokens":500,"total_tokens":2500}}`)
	c, ok := ParseUsageCostFromBody(body)
	if !ok || c != 2 {
		t.Fatalf("got cost=%d ok=%v", c, ok)
	}
	chat := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":50}}`)
	c, ok = ParseUsageCostFromBody(chat)
	if !ok || c != 1 {
		t.Fatalf("chat cost=%d ok=%v", c, ok)
	}
	if _, ok := ParseUsageCostFromBody([]byte(`{"no":"usage"}`)); ok {
		t.Fatal("expected fail")
	}
}

func TestParseUsageCostFromSSE(t *testing.T) {
	sse := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3000,\"output_tokens\":1000}}}\n\n")
	c, ok := ParseUsageCostFromSSE(sse)
	if !ok || c != 4 {
		t.Fatalf("sse cost=%d ok=%v", c, ok)
	}
}
