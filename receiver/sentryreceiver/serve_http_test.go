package sentryreceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type tracesSink struct {
	err    error
	traces ptrace.Traces
}

func (s *tracesSink) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{}
}

func (s *tracesSink) ConsumeTraces(_ context.Context, td ptrace.Traces) error {
	s.traces = td
	return s.err
}

func newTestHTTPReceiver(t *testing.T, sink *tracesSink) *sentrytraceReceiver {
	t.Helper()

	settings := receiver.Settings{
		ID: component.MustNewID(typeStr),
		TelemetrySettings: component.TelemetrySettings{
			Logger:         zap.NewNop(),
			MeterProvider:  noop.NewMeterProvider(),
			TracerProvider: tracenoop.NewTracerProvider(),
		},
	}
	sr, err := newReceiver(&Config{}, sink, settings)
	if err != nil {
		t.Fatalf("newReceiver returned error: %v", err)
	}
	return sr
}

func TestServeHTTPParseError(t *testing.T) {
	sr := newTestHTTPReceiver(t, &tracesSink{})
	req := httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader("bad"))
	rec := httptest.NewRecorder()

	sr.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "{}" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestServeHTTPSuccessForEventAndSession(t *testing.T) {
	eventSink := &tracesSink{}
	eventReceiver := newTestHTTPReceiver(t, eventSink)
	eventBody := strings.Join([]string{
		`{"event_id":"0123456789abcdef0123456789abcdef"}`,
		`{"type":"event"}`,
		`{"event_id":"0123456789abcdef0123456789abcdef","message":"boom","timestamp":123.4,"contexts":{"trace":{"span_id":"0123456789abcdef","trace_id":"0123456789abcdef0123456789abcdef"}}}`,
		"",
	}, "\n")

	eventReq := httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader(eventBody))
	eventRec := httptest.NewRecorder()
	eventReceiver.ServeHTTP(eventRec, eventReq)

	if eventRec.Code != http.StatusOK {
		t.Fatalf("unexpected event status code: %d", eventRec.Code)
	}
	if body := strings.TrimSpace(eventRec.Body.String()); body != `{"id":"0123456789abcdef0123456789abcdef"}` {
		t.Fatalf("unexpected event body: %q", body)
	}
	if eventSink.traces.SpanCount() != 1 {
		t.Fatalf("expected one event span, got %d", eventSink.traces.SpanCount())
	}

	sessionSink := &tracesSink{}
	sessionReceiver := newTestHTTPReceiver(t, sessionSink)
	sessionBody := strings.Join([]string{
		`{"event_id":"evt-2"}`,
		`{"type":"session"}`,
		`{"sid":"550e8400-e29b-41d4-a716-446655440000","status":"exited","timestamp":"2025-01-02T03:04:05Z"}`,
		"",
	}, "\n")

	sessionReq := httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader(sessionBody))
	sessionRec := httptest.NewRecorder()
	sessionReceiver.ServeHTTP(sessionRec, sessionReq)

	if sessionRec.Code != http.StatusOK {
		t.Fatalf("unexpected session status code: %d", sessionRec.Code)
	}
	if body := strings.TrimSpace(sessionRec.Body.String()); body != "{}" {
		t.Fatalf("unexpected session body: %q", body)
	}
	if sessionSink.traces.SpanCount() != 1 {
		t.Fatalf("expected one session span, got %d", sessionSink.traces.SpanCount())
	}
}

func TestServeHTTPConsumerErrors(t *testing.T) {
	body := strings.Join([]string{
		`{"event_id":"0123456789abcdef0123456789abcdef"}`,
		`{"type":"event"}`,
		`{"event_id":"0123456789abcdef0123456789abcdef","message":"boom","timestamp":123.4,"contexts":{"trace":{"span_id":"0123456789abcdef","trace_id":"0123456789abcdef0123456789abcdef"}}}`,
		"",
	}, "\n")

	permanentSink := &tracesSink{err: consumererror.NewPermanent(errors.New("bad request"))}
	permanentReceiver := newTestHTTPReceiver(t, permanentSink)
	permanentReq := httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader(body))
	permanentRec := httptest.NewRecorder()
	permanentReceiver.ServeHTTP(permanentRec, permanentReq)

	if permanentRec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected permanent error status code: %d", permanentRec.Code)
	}
	if got := strings.TrimSpace(permanentRec.Body.String()); got != string(errBadRequestRespBody) {
		t.Fatalf("unexpected permanent error body: %q", got)
	}

	transientSink := &tracesSink{err: errors.New("backend unavailable")}
	transientReceiver := newTestHTTPReceiver(t, transientSink)
	transientReq := httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader(body))
	transientRec := httptest.NewRecorder()
	transientReceiver.ServeHTTP(transientRec, transientReq)

	if transientRec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected transient error status code: %d", transientRec.Code)
	}
	if got := strings.TrimSpace(transientRec.Body.String()); got != string(errNextConsumerRespBody) {
		t.Fatalf("unexpected transient error body: %q", got)
	}
}

func TestServeHTTPLogsExcludeTelemetryPayloads(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{"event", `{"dsn":"https://secret-dsn@example.test/1"}
{"type":"event"}
{"event_id":"0123456789abcdef0123456789abcdef","message":"secret-message","timestamp":123.4,"request":{"headers":{"Authorization":"Bearer secret-token"}},"contexts":{"trace":{"span_id":"0123456789abcdef","trace_id":"0123456789abcdef0123456789abcdef"}}}
`, http.StatusOK},
		{"invalid request URL", `{}
{"type":"event"}
{"event_id":"0123456789abcdef0123456789abcdef","timestamp":123.4,"request":{"url":"http://example.test/%secret-path?token=secret-token"},"contexts":{"trace":{"span_id":"0123456789abcdef","trace_id":"0123456789abcdef0123456789abcdef"}}}
`, http.StatusOK},
		{"query parameter", `{}
{"type":"event"}
{"event_id":"0123456789abcdef0123456789abcdef","timestamp":123.4,"request":{"url":"http://example.test/?token=secret-token"},"contexts":{"trace":{"span_id":"0123456789abcdef","trace_id":"0123456789abcdef0123456789abcdef"}}}
`, http.StatusOK},
		{"invalid event", `{}
{"type":"event"}
{"Authorization":"Bearer secret-token","timestamp":"invalid"}
`, http.StatusNotAcceptable},
		{"invalid session", `{}
{"type":"session"}
{"Authorization":"Bearer secret-token","timestamp":42}
`, http.StatusNotAcceptable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			sr := newTestHTTPReceiver(t, &tracesSink{})
			sr.config.HttpQueryParamValuesToAttrs = []string{"token"}
			sr.logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)).Named("sentryreceiver")
			rec := httptest.NewRecorder()
			sr.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/frontend/api", strings.NewReader(tt.body)))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if output.Len() == 0 {
				t.Fatal("expected diagnostic records")
			}
			if strings.Contains(output.String(), "secret-") {
				t.Fatalf("telemetry leaked to logs: %s", output.String())
			}
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var entry map[string]any
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatalf("invalid JSON record %q: %v", line, err)
				}
				if entry["logger"] != "sentryreceiver" {
					t.Fatalf("logger = %v, want sentryreceiver", entry["logger"])
				}
			}
		})
	}
}
