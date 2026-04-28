package sentryreceiver

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Netcracker/qubership-open-telemetry-collector/receiver/sentryreceiver/models"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

func TestProcessBodyIfNecessary(t *testing.T) {
	plainReq := httptest.NewRequest("POST", "/", io.NopCloser(bytes.NewBufferString("plain")))
	if got, _ := io.ReadAll(processBodyIfNecessary(plainReq)); string(got) != "plain" {
		t.Fatalf("unexpected plain body: %q", string(got))
	}

	var gz bytes.Buffer
	gzw := gzip.NewWriter(&gz)
	_, _ = gzw.Write([]byte("gzip-body"))
	_ = gzw.Close()
	gzipReq := httptest.NewRequest("POST", "/", io.NopCloser(bytes.NewReader(gz.Bytes())))
	gzipReq.Header.Set("Content-Encoding", "gzip")
	if got, _ := io.ReadAll(processBodyIfNecessary(gzipReq)); string(got) != "gzip-body" {
		t.Fatalf("unexpected gzip body: %q", string(got))
	}

	var zl bytes.Buffer
	zw := zlib.NewWriter(&zl)
	_, _ = zw.Write([]byte("zlib-body"))
	_ = zw.Close()
	zlibReq := httptest.NewRequest("POST", "/", io.NopCloser(bytes.NewReader(zl.Bytes())))
	zlibReq.Header.Set("Content-Encoding", "deflate")
	if got, _ := io.ReadAll(processBodyIfNecessary(zlibReq)); string(got) != "zlib-body" {
		t.Fatalf("unexpected zlib body: %q", string(got))
	}

	// gzip.NewReader/zlib.NewReader may consume bytes before returning an error,
	// so the fallback guarantee here is "no panic and a readable fallback reader",
	// not preservation of the original payload.
	if got, err := io.ReadAll(gunzippedBodyIfPossible(bytes.NewBufferString("bad-gzip"))); err != nil || got == nil {
		t.Fatalf("expected readable gzip fallback, got data=%q err=%v", string(got), err)
	}
	if got, err := io.ReadAll(zlibUncompressedbody(bytes.NewBufferString("bad-zlib"))); err != nil || got == nil {
		t.Fatalf("expected readable zlib fallback, got data=%q err=%v", string(got), err)
	}
}

func TestTraceHelperFunctions(t *testing.T) {
	sr := &sentrytraceReceiver{
		logger: zap.NewNop(),
		config: &Config{LevelEvaluationStrategy: "max"},
	}

	event := models.Event{
		Level: "info",
		Breadcrumbs: []models.Breadcrumb{
			{Level: "debug"},
			{Level: "error"},
		},
	}
	if level := sr.evaluateLevel(event); level != "error" {
		t.Fatalf("unexpected evaluated level: %q", level)
	}
	sr.config.LevelEvaluationStrategy = ""
	if level := sr.evaluateLevel(event); level != "info" {
		t.Fatalf("unexpected passthrough level: %q", level)
	}

	traceID := sr.GenerateTraceID("0123456789abcdef0123456789abcdef")
	if traceID.String() != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected trace id: %s", traceID.String())
	}
	if got := sr.GenerateTraceID("bad"); got != (ptrace.NewSpan().TraceID()) {
		t.Fatalf("expected zero trace id, got %v", got)
	}

	spanID := sr.GenerateSpanId("0123456789abcdef")
	if spanID.String() != "0123456789abcdef" {
		t.Fatalf("unexpected span id: %s", spanID.String())
	}
	if got := sr.GenerateSpanId("bad"); got != (ptrace.NewSpan().SpanID()) {
		t.Fatalf("expected zero span id, got %v", got)
	}

	ts := GetUnixTimeFromFloat64(123.25)
	if ts.Unix() != 123 || ts.Nanosecond() != 250000000 {
		t.Fatalf("unexpected converted time: %v", ts)
	}

	if got := sr.removeIdFromURL("https://example.com/orders/123/items/550e8400-e29b-41d4-a716-446655440000"); got != "https://example.com/orders/_NUMBER_/items/_UUID_" {
		t.Fatalf("unexpected sanitized URL: %q", got)
	}
	if got := sr.removeIdFromURL("/orders/1234/alpha-12345678"); got != "/orders/_NUMBER_/_ID_" {
		t.Fatalf("unexpected path sanitizing: %q", got)
	}
	if got := sr.removeIdFromURL("http://%"); got != "NON_PARSABLE_URL" {
		t.Fatalf("unexpected invalid URL result: %q", got)
	}

	if got := removeHyphens("a-b-c"); got != "abc" {
		t.Fatalf("unexpected removeHyphens result: %q", got)
	}

	req := httptest.NewRequest("POST", "/frontend/api/v1", nil)
	if got := sr.GetServiceName(req); got != "frontend" {
		t.Fatalf("unexpected service name from path: %q", got)
	}
	req.Header.Set("x-service-id", "header-service")
	if got := sr.GetServiceName(req); got != "header-service" {
		t.Fatalf("unexpected service name from header: %q", got)
	}
}

func TestToTraceSpansBuildsTransactionAndSessionSpans(t *testing.T) {
	sr := &sentrytraceReceiver{
		logger: zap.NewNop(),
		config: &Config{
			HttpQueryParamValuesToAttrs:    []string{"feature"},
			HttpQueryParamExistenceToAttrs: []string{"debug"},
			LevelEvaluationStrategy:        "max",
			ContextSpanAttributesList:      []string{"custom"},
		},
	}

	req := httptest.NewRequest("POST", "/frontend/api?feature=on&debug=1", nil)
	req.Header.Set("x-service-name", "frontend-service")

	transactionEnv := &models.EnvelopEventParseResult{
		EnvelopType: models.ENVELOP_TYPE_TRANSACTION,
		Events: []models.Event{
			{
				EventId:        "0123456789abcdef0123456789abcdef",
				Transaction:    "/orders/123",
				Timestamp:      200,
				StartTimestamp: 100,
				Request:        models.EventRequest{URL: "https://example.com/orders/123?feature=on&debug=1"},
				Contexts: models.EventContexts{
					AsMap: map[string]interface{}{
						"custom": map[string]interface{}{"tenant": "acme"},
					},
				},
				Measurements: map[string]models.EventMeasurement{
					"fp": {Value: 1500, Unit: "millisecond"},
				},
				Spans: []models.EventSpan{
					{
						SpanId:         "0123456789abcdef",
						ParentSpanId:   "1111111111111111",
						TraceId:        "0123456789abcdef0123456789abcdef",
						Op:             "http.client",
						Description:    "fetch",
						Origin:         "auto.http",
						StartTimestamp: 110,
						Timestamp:      120,
						Data: map[string]interface{}{
							"http.response.status_code": 200.0,
							"url":                       "https://example.com/orders/123",
							"http.request.fetch_start":  10.5,
							"retries":                   2.0,
							"ratio":                     1.25,
						},
						Tags: map[string]interface{}{"kind": "external"},
					},
				},
			},
		},
	}
	transactionEnv.Events[0].Contexts.Trace.Op = "pageload"
	transactionEnv.Events[0].Contexts.Trace.SpanID = "1111111111111111"
	transactionEnv.Events[0].Contexts.Trace.TraceID = "0123456789abcdef0123456789abcdef"

	traces, err := sr.toTraceSpans(transactionEnv, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	if spans.Len() != 2 {
		t.Fatalf("expected transaction root span plus child span, got %d", spans.Len())
	}

	root := spans.At(0)
	if root.Name() != "/orders/_NUMBER_ pageload" {
		t.Fatalf("unexpected root span name: %q", root.Name())
	}
	if got, _ := root.Attributes().Get("http.qparam.feature"); got.AsString() != "on" {
		t.Fatalf("expected feature query param attribute, got %v", got.AsString())
	}
	if got, _ := root.Attributes().Get("http.qparam.debug"); got.AsString() != "true" {
		t.Fatalf("expected debug query param attribute, got %v", got.AsString())
	}
	if got, _ := root.Attributes().Get("contexts.custom.tenant"); got.AsString() != "acme" {
		t.Fatalf("expected custom context attribute, got %v", got.AsString())
	}

	child := spans.At(1)
	if child.Status().Code() != ptrace.StatusCodeOk {
		t.Fatalf("expected child span status OK, got %v", child.Status().Code())
	}
	if got, _ := child.Attributes().Get("url_path"); got.AsString() != "https://example.com/orders/_NUMBER_" {
		t.Fatalf("unexpected child url path: %v", got.AsString())
	}
	if got, _ := child.Attributes().Get("retries"); got.Int() != 2 {
		t.Fatalf("expected integer retry attribute, got %v", got.Int())
	}

	sessionEnv := &models.EnvelopEventParseResult{
		EnvelopType: models.ENVELOP_TYPE_SESSION,
		SessionEvents: []models.SessionEvent{
			{
				Sid:       "550e8400-e29b-41d4-a716-446655440000",
				Status:    "exited",
				Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339),
			},
		},
	}
	sessionTraces, err := sr.toTraceSpans(sessionEnv, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sessionSpan := sessionTraces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	if sessionSpan.Name() != "Session 550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("unexpected session span name: %q", sessionSpan.Name())
	}
	if got, _ := sessionSpan.Attributes().Get("session.status"); got.AsString() != "exited" {
		t.Fatalf("unexpected session status: %v", got.AsString())
	}
}
