package logtcpexporter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

func startGraylogCapture(t *testing.T) (string, <-chan map[string]any, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}

	messages := make(chan map[string]any, 16)
	acceptDone := make(chan struct{})

	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					payload, err := reader.ReadBytes(0)
					if len(payload) > 1 {
						var msg map[string]any
						if unmarshalErr := json.Unmarshal(payload[:len(payload)-1], &msg); unmarshalErr != nil {
							t.Errorf("failed to decode captured GELF payload: %v", unmarshalErr)
							return
						}
						messages <- msg
					}
					if err != nil {
						if !errors.Is(err, io.EOF) {
							t.Errorf("failed to read captured GELF payload: %v", err)
						}
						return
					}
				}
			}(conn)
		}
	}()

	cleanup := func() {
		_ = listener.Close()
		<-acceptDone
	}

	return listener.Addr().String(), messages, cleanup
}

func mustReadCapturedMessage(t *testing.T, messages <-chan map[string]any) map[string]any {
	t.Helper()

	select {
	case msg := <-messages:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for graylog message")
		return nil
	}
}

func mustNotReadCapturedMessage(t *testing.T, messages <-chan map[string]any) {
	t.Helper()

	select {
	case msg := <-messages:
		t.Fatalf("did not expect a graylog message, got %#v", msg)
	case <-time.After(250 * time.Millisecond):
	}
}

func newStartedExporter(t *testing.T, cfg *Config) (*logTcpExporter, <-chan map[string]any, func()) {
	t.Helper()

	endpoint, messages, cleanupListener := startGraylogCapture(t)
	cfg.Endpoint = endpoint

	lte := createLogTcpExporter(cfg, exporter.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
	})
	if err := lte.start(context.Background(), nil); err != nil {
		cleanupListener()
		t.Fatalf("start returned error: %v", err)
	}

	cleanup := func() {
		lte.graylogSender.Stop()
		cleanupListener()
	}

	return lte, messages, cleanup
}

func newTraceID() pcommon.TraceID {
	return pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
}

func newSpanID() pcommon.SpanID {
	return pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
}

func TestTraceAndSpanHelpers(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.TraceFilters = []ATLFilter{{Tags: map[string]string{"deployment.environment": "prod"}}}
	cfg.ATLCfg.SpanFilters = []ATLFilter{{
		ServiceNames: []string{"frontend"},
		Tags:         map[string]string{"kind": "server"},
	}}
	lte := createLogTcpExporter(cfg, exporter.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
	})

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("trace.source.type", "sentry")
	rs.Resource().Attributes().PutStr("deployment.environment", "prod")
	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetTraceID(newTraceID())
	span.SetSpanID(newSpanID())
	span.SetParentSpanID(pcommon.SpanID([8]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	span.SetName("frontend-request")
	span.Attributes().PutStr("service.name", "frontend")
	span.Attributes().PutStr("kind", "server")
	span.Attributes().PutStr("message", "hello")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(120, 0)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(123, 0)))
	span.Status().SetCode(ptrace.StatusCodeError)

	if !isSentryTrace(traces) {
		t.Fatal("expected trace to be recognized as sentry trace")
	}
	if got := lte.getTraceId(traces); got != span.TraceID().String() {
		t.Fatalf("unexpected trace id: %q", got)
	}
	if _, level := lte.getTimestampAndLevel(traces); level != 3 {
		t.Fatalf("unexpected trace level: %d", level)
	}
	if idx := lte.getATLTraceFilterIndex(traces); idx != 0 {
		t.Fatalf("unexpected trace filter index: %d", idx)
	}
	if idx := lte.getATLSpanFilterIndex(span); idx != 0 {
		t.Fatalf("unexpected span filter index: %d", idx)
	}
	if got := lte.getStringFromSpanFields(span, []string{"message", "__name__"}); got != "hello\nfrontend-request" {
		t.Fatalf("unexpected string mapping: %q", got)
	}
	if got := lte.getTimeFromSpanFields(span, []string{"__endTime__"}); got != 123 {
		t.Fatalf("unexpected mapped timestamp: %d", got)
	}

	emptySpan := ptrace.NewSpan()
	if got := lte.getStringFromSpanFields(emptySpan, []string{"missing"}); got != "" {
		t.Fatalf("expected empty mapping result, got %q", got)
	}
}

func TestSendArbitraryLoggingSpanSendsMappedMessage(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.SpanFilters = []ATLFilter{{
		ServiceNames: []string{"frontend"},
		Tags:         map[string]string{"kind": "server"},
		Mapping: map[string][]string{
			"__message__":   []string{"message"},
			"__host__":      []string{"service.name"},
			"__timestamp__": []string{"__endTime__"},
			"trace_id":      []string{"__traceId__"},
		},
	}}

	lte, messages, cleanup := newStartedExporter(t, cfg)
	defer cleanup()

	span := ptrace.NewSpan()
	span.SetTraceID(newTraceID())
	span.SetSpanID(newSpanID())
	span.Attributes().PutStr("service.name", "frontend")
	span.Attributes().PutStr("kind", "server")
	span.Attributes().PutStr("message", "hello from span")
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(123, 0)))
	span.Status().SetCode(ptrace.StatusCodeError)

	if err := lte.sendArbitraryLoggingSpan(span); err != nil {
		t.Fatalf("sendArbitraryLoggingSpan returned error: %v", err)
	}

	msg := mustReadCapturedMessage(t, messages)
	if got := msg["host"]; got != "frontend" {
		t.Fatalf("unexpected host: %#v", got)
	}
	if got := msg["short_message"]; got != "hello from span" {
		t.Fatalf("unexpected short_message: %#v", got)
	}
	if got := msg["timestamp"]; got != float64(123) {
		t.Fatalf("unexpected timestamp: %#v", got)
	}
	if got := msg["level"]; got != float64(3) {
		t.Fatalf("unexpected level: %#v", got)
	}
	if got := msg["_trace_id"]; got != span.TraceID().String() {
		t.Fatalf("unexpected _trace_id: %#v", got)
	}
}

func TestPushTracesSendsSentryEventAndBreadcrumbMessages(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	lte, messages, cleanup := newStartedExporter(t, cfg)
	defer cleanup()

	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("trace.source.type", "sentry")
	ss := rs.ScopeSpans().AppendEmpty()
	span := ss.Spans().AppendEmpty()
	span.SetName("Event")
	span.Attributes().PutStr("contexts.trace.span_id", "0123456789abcdef")
	span.Attributes().PutStr("contexts.trace.trace_id", "0123456789abcdef0123456789abcdef")
	span.Attributes().PutStr("level", "error")
	span.Attributes().PutStr("message", "frontend failure")
	span.Attributes().PutStr("version", "1.0.0")
	span.Attributes().PutStr("category", "console")
	span.Attributes().PutDouble("timestamp", 123)
	span.Attributes().PutStr("url", "https://example.com/orders/123")

	breadcrumbs := span.Attributes().PutEmptySlice("breadcrumbs")
	breadcrumb := breadcrumbs.AppendEmpty().SetEmptyMap()
	breadcrumb.PutStr("level", "info")
	breadcrumb.PutDouble("timestamp", 124)
	breadcrumb.PutStr("category", "console")
	breadcrumb.PutStr("message", "user clicked button")

	if err := lte.pushTraces(context.Background(), traces); err != nil {
		t.Fatalf("pushTraces returned error: %v", err)
	}

	mainMessage := mustReadCapturedMessage(t, messages)
	breadcrumbMessage := mustReadCapturedMessage(t, messages)

	if got := mainMessage["short_message"]; got != "frontend failure" {
		t.Fatalf("unexpected main short_message: %#v", got)
	}
	if got := mainMessage["host"]; got != "user_browser" {
		t.Fatalf("unexpected main host: %#v", got)
	}
	if got := mainMessage["level"]; got != float64(3) {
		t.Fatalf("unexpected main level: %#v", got)
	}
	if got := mainMessage["_category"]; got != "console" {
		t.Fatalf("unexpected main category: %#v", got)
	}

	if got := breadcrumbMessage["short_message"]; got != "user clicked button" {
		t.Fatalf("unexpected breadcrumb short_message: %#v", got)
	}
	if got := breadcrumbMessage["level"]; got != float64(6) {
		t.Fatalf("unexpected breadcrumb level: %#v", got)
	}
	if got := breadcrumbMessage["_trace_id"]; got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected breadcrumb trace id: %#v", got)
	}
}

func TestSendArbitraryLoggingSpanSkipsFilteredOutSpan(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.SpanFilters = []ATLFilter{{
		ServiceNames: []string{"frontend"},
		Tags:         map[string]string{"kind": "server"},
		Mapping: map[string][]string{
			"__message__": []string{"message"},
		},
	}}

	lte, messages, cleanup := newStartedExporter(t, cfg)
	defer cleanup()

	span := ptrace.NewSpan()
	span.Attributes().PutStr("service.name", "backend")
	span.Attributes().PutStr("kind", "server")
	span.Attributes().PutStr("message", "should not be sent")

	if err := lte.sendArbitraryLoggingSpan(span); err != nil {
		t.Fatalf("sendArbitraryLoggingSpan returned error: %v", err)
	}

	mustNotReadCapturedMessage(t, messages)
}
