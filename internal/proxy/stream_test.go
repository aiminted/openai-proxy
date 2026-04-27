package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSSEUsage(t *testing.T) {
	body := `data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"delta":{"content":"hi"}}]}
data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4}}}
data: [DONE]
`
	u, ok := ParseSSEUsage([]byte(body))
	if !ok {
		t.Fatal("expected usage to be found")
	}
	if u.InputTokens != 12 || u.OutputTokens != 8 || u.CachedTokens != 4 || u.Model != "gpt-4o" {
		t.Errorf("unexpected usage: %+v", u)
	}
}

func TestParseJSONUsage(t *testing.T) {
	body := `{"id":"chatcmpl-1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":30}}}`
	u, ok := ParseJSONUsage([]byte(body))
	if !ok {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.CachedTokens != 30 || u.Model != "gpt-4o" {
		t.Errorf("unexpected usage: %+v", u)
	}
}

func TestPrepareBodyInjectsStreamOptions(t *testing.T) {
	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	pb, err := PrepareBody(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !pb.IsStream || !pb.Modified {
		t.Errorf("expected stream + modified, got %+v", pb)
	}
	if !bytes.Contains(pb.Body, []byte("include_usage")) {
		t.Errorf("expected include_usage in body, got %s", string(pb.Body))
	}
	if pb.Model != "gpt-4o" {
		t.Errorf("model not extracted: %q", pb.Model)
	}
}

func TestPrepareBodyPreservesExistingStreamOptions(t *testing.T) {
	body := `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
	pb, err := PrepareBody(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if pb.Modified {
		t.Errorf("should not modify when include_usage already true")
	}
	if !pb.IsStream {
		t.Errorf("expected stream")
	}
}

func TestPrepareBodyNonJSON(t *testing.T) {
	body := "not json at all"
	pb, err := PrepareBody(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if pb.WasJSON || pb.Modified {
		t.Errorf("expected non-json untouched: %+v", pb)
	}
}

func TestPricingLookupFallback(t *testing.T) {
	// sanity: ensure tail buffer truncates correctly
	tb := newTailBuffer(8)
	tb.Write([]byte("abcdefghijklmnop"))
	if string(tb.Bytes()) != "ijklmnop" {
		t.Errorf("tail buffer wrong: %q", string(tb.Bytes()))
	}
}
