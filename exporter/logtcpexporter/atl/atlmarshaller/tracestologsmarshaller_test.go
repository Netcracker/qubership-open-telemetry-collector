package atlmarshaller

import (
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestValueToString(t *testing.T) {
	if got := valueToString(pcommon.NewValueStr("hello")); got != "Str(hello)" {
		t.Fatalf("unexpected string conversion: %q", got)
	}
	if got := valueToString(pcommon.NewValueInt(42)); got != "Int(42)" {
		t.Fatalf("unexpected int conversion: %q", got)
	}
}

func TestMarshalTracesIncludesResourceSpanEventsAndLinks(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.SetSchemaUrl("resource-schema")
	rs.Resource().Attributes().PutStr("service.name", "frontend")

	ss := rs.ScopeSpans().AppendEmpty()
	ss.SetSchemaUrl("scope-schema")
	ss.Scope().SetName("ui-scope")
	ss.Scope().SetVersion("1.0.0")
	ss.Scope().Attributes().PutStr("scope.attr", "value")

	span := ss.Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID([16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}))
	span.SetSpanID(pcommon.SpanID([8]byte{2, 2, 2, 2, 2, 2, 2, 2}))
	span.SetParentSpanID(pcommon.SpanID([8]byte{3, 3, 3, 3, 3, 3, 3, 3}))
	span.SetName("GET /orders")
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(100, 0)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(101, 0)))
	span.Status().SetCode(ptrace.StatusCodeError)
	span.Status().SetMessage("boom")
	span.Attributes().PutStr("http.method", "GET")
	span.Attributes().PutInt("http.status_code", 500)

	event := span.Events().AppendEmpty()
	event.SetName("exception")
	event.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(100, 500)))
	event.SetDroppedAttributesCount(1)
	event.Attributes().PutStr("exception.type", "TypeError")

	link := span.Links().AppendEmpty()
	link.SetTraceID(pcommon.TraceID([16]byte{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4}))
	link.SetSpanID(pcommon.SpanID([8]byte{5, 5, 5, 5, 5, 5, 5, 5}))
	link.TraceState().FromRaw("vendor=value")
	link.SetDroppedAttributesCount(2)
	link.Attributes().PutStr("link.attr", "linked")

	data, err := MarshalTraces(traces)
	if err != nil {
		t.Fatalf("MarshalTraces returned error: %v", err)
	}

	out := string(data)
	expectedSubstrings := []string{
		"ResourceSpans #0",
		"Resource SchemaURL: resource-schema",
		"Resource attributes:",
		"service.name: Str(frontend)",
		"ScopeSpans #0",
		"ScopeSpans SchemaURL: scope-schema",
		"InstrumentationScope ui-scope 1.0.0",
		"scope.attr: Str(value)",
		"Span #0",
		"Name           : GET /orders",
		"Kind           : Client",
		"Status code    : Error",
		"Status message : boom",
		"http.method: Str(GET)",
		"http.status_code: Int(500)",
		"Events:",
		"SpanEvent #0",
		"exception.type: Str(TypeError)",
		"Links:",
		"SpanLink #0",
		"TraceState: vendor=value",
		"link.attr: Str(linked)",
	}
	for _, want := range expectedSubstrings {
		if !strings.Contains(out, want) {
			t.Fatalf("expected marshalled traces to contain %q, got:\n%s", want, out)
		}
	}
}

func TestMarshalTracesWithEmptyData(t *testing.T) {
	data, err := MarshalTraces(ptrace.NewTraces())
	if err != nil {
		t.Fatalf("MarshalTraces returned error: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty output for empty traces, got %q", string(data))
	}
}
