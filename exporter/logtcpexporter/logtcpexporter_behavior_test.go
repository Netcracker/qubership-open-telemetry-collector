package logtcpexporter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Netcracker/qubership-open-telemetry-collector/common/graylog"
)

func closeCapturedConn(t *testing.T, c net.Conn) {
	t.Helper()

	if err := c.Close(); err != nil {
		t.Errorf("failed to close captured connection: %v", err)
	}
}

func decodeCapturedPayload(payload []byte) (map[string]any, error) {
	var msg map[string]any
	if err := json.Unmarshal(payload[:len(payload)-1], &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func readCapturedConn(t *testing.T, c net.Conn, messages chan<- map[string]any) {
	t.Helper()

	defer closeCapturedConn(t, c)

	reader := bufio.NewReader(c)
	for {
		payload, err := reader.ReadBytes(0)
		if len(payload) > 1 {
			msg, decodeErr := decodeCapturedPayload(payload)
			if decodeErr != nil {
				t.Errorf("failed to decode captured GELF payload: %v", decodeErr)
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
}

func acceptCapturedConns(t *testing.T, listener net.Listener, messages chan<- map[string]any, acceptDone chan<- struct{}) {
	t.Helper()

	defer close(acceptDone)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go readCapturedConn(t, conn, messages)
	}
}

func startGraylogCapture(t *testing.T) (string, <-chan map[string]any, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}

	messages := make(chan map[string]any, 16)
	acceptDone := make(chan struct{})

	go acceptCapturedConns(t, listener, messages, acceptDone)

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

func newJSONLoggedExporter(t *testing.T, cfg *Config) (*logTcpExporter, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel))
	return createLogTcpExporter(cfg, exporter.Settings{TelemetrySettings: component.TelemetrySettings{Logger: logger}}), &output
}

func requireJSONLog(t *testing.T, output *bytes.Buffer, message string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var found map[string]any
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatalf("decode structured log: %v; output: %s", err, output)
		}
		if entry["msg"] == message {
			if found != nil {
				t.Fatalf("log %q emitted more than once: %s", message, output)
			}
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("log %q missing from %s", message, output)
	}
	return found
}

func fullGraylogQueue(t *testing.T) *graylog.GraylogSender {
	t.Helper()
	// No consumers keeps backpressure deterministic without racing a TCP worker.
	sender := graylog.NewGraylogSender(graylog.Endpoint{Transport: graylog.TCP}, zap.NewNop(), 0, 1, 0, 0, 0)
	t.Cleanup(sender.Stop)
	if err := sender.SendToQueue(&graylog.Message{ShortMessage: "occupied"}); err != nil {
		t.Fatalf("fill queue: %v", err)
	}
	return sender
}

func TestStartInvalidConfigurationLogsJSONFields(t *testing.T) {
	for _, tt := range []struct {
		name, endpoint, freezeTime, message, field, value string
	}{
		{"invalid port", "collector.invalid:bad\"\\\nport", "1s", "Invalid port in endpoint", "port", "bad\"\\\nport"},
		{"out of range port", "collector.invalid:65536", "1s", "Invalid port in endpoint", "port", "65536"},
		{"invalid freeze time", "collector.invalid:12201", "bad\"\\\nduration", "Cannot parse successiveSendErrFreezeTime", "value", "bad\"\\\nduration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig().(*Config)
			cfg.Endpoint, cfg.SuccessiveSendErrFreezeTime = tt.endpoint, tt.freezeTime
			lte, output := newJSONLoggedExporter(t, cfg)
			if err := lte.start(context.Background(), nil); err == nil {
				t.Fatal("start(invalid configuration) returned nil, want error")
			}
			if lte.graylogSender != nil {
				t.Fatal("start(invalid configuration) created a sender")
			}
			entry := requireJSONLog(t, output, tt.message)
			if entry["level"] != "error" || entry[tt.field] != tt.value {
				t.Fatalf("invalid configuration log = %#v, want error with %s=%q", entry, tt.field, tt.value)
			}
		})
	}
}

func TestPushTracesReturnsQueueFailureWithoutLoggingTracePayload(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.TraceFilters = []ATLFilter{{Tags: map[string]string{"deployment.environment": "prod"}}}
	lte, output := newJSONLoggedExporter(t, cfg)
	lte.graylogSender = fullGraylogQueue(t)
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("deployment.environment", "prod")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.Attributes().PutStr("authorization", "private-trace-payload")
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(123, 0)))

	if err := lte.pushTraces(context.Background(), traces); err == nil {
		t.Fatal("pushTraces(full queue) returned nil, want error")
	}
	entry := requireJSONLog(t, output, "Failed to put message on the graylog queue")
	if entry["level"] != "error" || entry["msg_timestamp"] != float64(123) || entry["error"] == nil {
		t.Fatalf("queue failure log = %#v, want error, timestamp 123, and cause", entry)
	}
	if strings.Contains(output.String(), "private-trace-payload") {
		t.Fatal("queue failure log contains trace payload")
	}
}

func TestPushTracesLogsSpanQueueFailureWithoutLoggingPayload(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.SpanFilters = []ATLFilter{{Mapping: map[string][]string{"__message__": {"message"}}}}
	lte, output := newJSONLoggedExporter(t, cfg)
	lte.graylogSender = fullGraylogQueue(t)
	traces := ptrace.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.Attributes().PutStr("message", "private-span-payload")
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(124, 0)))

	if err := lte.pushTraces(context.Background(), traces); err != nil {
		t.Fatalf("pushTraces(best-effort span) = %v, want nil", err)
	}
	entry := requireJSONLog(t, output, "Failed to put message on the graylog queue")
	if entry["level"] != "error" || entry["msg_timestamp"] != float64(124) || entry["error"] == nil {
		t.Fatalf("span queue failure log = %#v, want error, timestamp 124, and cause", entry)
	}
	entry = requireJSONLog(t, output, "Failed to send arbitrary logging span")
	if entry["level"] != "error" || entry["error"] == nil {
		t.Fatalf("span send failure log = %#v, want error and cause", entry)
	}
	if strings.Contains(output.String(), "private-span-payload") {
		t.Fatal("span queue failure log contains message payload")
	}
}

func TestPushTracesLogsSentryQueueFailureWithIdentifiersOnly(t *testing.T) {
	lte, output := newJSONLoggedExporter(t, createDefaultConfig().(*Config))
	lte.graylogSender = fullGraylogQueue(t)
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("trace.source.type", "sentry")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("Event")
	span.Attributes().PutStr("contexts.trace.trace_id", "trace-123")
	span.Attributes().PutStr("contexts.trace.span_id", "span-123")
	span.Attributes().PutStr("message", "private-sentry-payload")

	if err := lte.pushTraces(context.Background(), traces); err != nil {
		t.Fatalf("pushTraces(best-effort Sentry event) = %v, want nil", err)
	}
	entry := requireJSONLog(t, output, "Failed to put message on the graylog queue")
	if entry["level"] != "error" || entry["trace_id"] != "trace-123" || entry["span_id"] != "span-123" || entry["error"] == nil {
		t.Fatalf("Sentry queue failure log = %#v, want error, trace/span IDs, and cause", entry)
	}
	requireJSONLog(t, output, "Failed to send sentry span")
	if strings.Contains(output.String(), "private-sentry-payload") {
		t.Fatal("Sentry queue failure log contains event payload")
	}
}

func TestSendSentrySpanBreadcrumbFailureLogsTypeAndIndexWithoutPayload(t *testing.T) {
	lte, output := newJSONLoggedExporter(t, createDefaultConfig().(*Config))
	// One slot accepts the event before its breadcrumb encounters backpressure.
	lte.graylogSender = graylog.NewGraylogSender(graylog.Endpoint{Transport: graylog.TCP}, zap.NewNop(), 0, 1, 0, 0, 0)
	t.Cleanup(lte.graylogSender.Stop)
	span := ptrace.NewSpan()
	span.Attributes().PutStr("contexts.trace.trace_id", "trace-456")
	span.Attributes().PutStr("contexts.trace.span_id", "span-456")
	span.Attributes().PutStr("message", "private-event-payload")
	span.Attributes().PutStr("level", "error")
	span.Attributes().PutEmptySlice("breadcrumbs").AppendEmpty().SetStr("private-breadcrumb-payload")

	if err := lte.sendSentrySpan(span); err == nil {
		t.Fatal("sendSentrySpan(full breadcrumb queue) returned nil, want error")
	}
	entry := requireJSONLog(t, output, "Type assertion error")
	if entry["level"] != "error" || entry["actual_type"] != "string" {
		t.Fatalf("invalid breadcrumb log = %#v, want error and actual_type=string", entry)
	}
	entry = requireJSONLog(t, output, "Failed to put breadcrumb message on the graylog queue")
	if entry["trace_id"] != "trace-456" || entry["span_id"] != "span-456" || entry["breadcrumb_index"] != float64(0) || entry["error"] == nil {
		t.Fatalf("breadcrumb failure log = %#v, want IDs, index 0, and cause", entry)
	}
	if strings.Contains(output.String(), "private-event-payload") || strings.Contains(output.String(), "private-breadcrumb-payload") {
		t.Fatal("breadcrumb failure log contains event or breadcrumb payload")
	}
}

func TestTraceFilteringLogsRejectionWithoutPayload(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.TraceFilters = []ATLFilter{{Tags: map[string]string{"deployment.environment": "prod"}}}
	lte, output := newJSONLoggedExporter(t, cfg)
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("deployment.environment", "dev")
	rs.Resource().Attributes().PutStr("authorization", "private-filtered-payload")

	if err := lte.pushTraces(context.Background(), traces); err != nil {
		t.Fatalf("pushTraces(filtered trace) = %v, want nil", err)
	}
	entry := requireJSONLog(t, output, "Trace filter evaluated")
	if entry["trace_filter_index"] != float64(0) || entry["matched"] != false {
		t.Fatalf("trace filter log = %#v, want filter 0 and matched=false", entry)
	}
	entry = requireJSONLog(t, output, "Trace is filtered out")
	if entry["al_index"] != float64(-1) {
		t.Fatalf("trace rejection log = %#v, want al_index=-1", entry)
	}
	if strings.Contains(output.String(), "private-filtered-payload") {
		t.Fatal("trace filtering log contains resource attributes")
	}
}

func TestEmptyMappedMessageLogsSpanIdentifiersWithoutPayload(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.ATLCfg.SpanFilters = []ATLFilter{{Mapping: map[string][]string{"__message__": {"missing"}}}}
	lte, output := newJSONLoggedExporter(t, cfg)
	span := ptrace.NewSpan()
	span.SetTraceID(newTraceID())
	span.SetSpanID(newSpanID())
	span.Attributes().PutStr("authorization", "private-empty-span-payload")

	if err := lte.sendArbitraryLoggingSpan(span); err != nil {
		t.Fatalf("sendArbitraryLoggingSpan(empty message) = %v, want nil", err)
	}
	entry := requireJSONLog(t, output, "Span is filtered out because message is empty")
	if entry["trace_id"] != span.TraceID().String() || entry["span_id"] != span.SpanID().String() {
		t.Fatalf("empty message log = %#v, want trace/span IDs", entry)
	}
	if strings.Contains(output.String(), "private-empty-span-payload") {
		t.Fatal("empty message log contains span attributes")
	}
}
