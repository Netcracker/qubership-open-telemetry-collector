package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrongStringUnmarshalJSON(t *testing.T) {
	var s StrongString
	if err := s.UnmarshalJSON([]byte(`"hello"`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(s) != `"hello"` {
		t.Fatalf("unexpected strong string content: %q", string(s))
	}
}

func TestStrongStringUnmarshalJSONTooLarge(t *testing.T) {
	var s StrongString
	large := `"` + strings.Repeat("a", 50*1024*1024) + `x"`
	if err := s.UnmarshalJSON([]byte(large)); err == nil {
		t.Fatal("expected size validation error")
	}
}

func TestEventContextsUnmarshalJSON(t *testing.T) {
	var ctx EventContexts
	raw := []byte(`{"trace":{"op":"http.client","span_id":"span","trace_id":"trace"},"Error":{"message":"boom"},"custom":{"nested":1}}`)
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Trace.Op != "http.client" {
		t.Fatalf("unexpected trace op: %q", ctx.Trace.Op)
	}
	if ctx.Error.Message != "boom" {
		t.Fatalf("unexpected error message: %q", ctx.Error.Message)
	}
	if _, ok := ctx.AsMap["custom"]; !ok {
		t.Fatalf("expected custom map in AsMap, got %#v", ctx.AsMap)
	}
}

func TestEventContextsUnmarshalJSONInvalid(t *testing.T) {
	var ctx EventContexts
	if err := json.Unmarshal([]byte(`{"trace":`), &ctx); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
