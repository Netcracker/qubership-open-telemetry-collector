package sentrymetricsconnector

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type metricsSink struct {
	metrics pmetric.Metrics
}

func (m *metricsSink) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{}
}

func (m *metricsSink) ConsumeMetrics(_ context.Context, md pmetric.Metrics) error {
	m.metrics = md
	return nil
}

func TestConnectorHelpersAndConsumeTraces(t *testing.T) {
	cfg := &Config{
		SentryMeasurementsCfg: SentryMeasurementsConfig{
			DefaultBuckets: []float64{100, 500},
			DefaultLabels:  map[string]string{"service": "service.name"},
			Custom: map[string]*CustomSentryMeasurementsConfig{
				"fp": {
					Buckets: []float64{10, 20},
					Labels:  &map[string]string{"path": "transaction_path"},
				},
			},
		},
		SentryEventCountCfg: SentryEventCountConfig{
			Labels: map[string]string{"level": "level"},
		},
	}
	sink := &metricsSink{}
	conn := CreateSentryMetricsConnector(cfg, sink, connector.Settings{
		ID: component.MustNewID("sentrymetrics"),
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
	})

	span := ptrace.NewSpan()
	span.Attributes().PutStr("service.name", "frontend")
	span.Attributes().PutStr("transaction_path", "/orders/_NUMBER_")
	if got := conn.getLabels(span, map[string]string{"service": "service.name", "missing": "missing"}); got["service"] != "frontend" || got["missing"] != "" {
		t.Fatalf("unexpected labels: %#v", got)
	}
	if got := conn.getConfigurableMeasurementLabels(span, "fp"); got["path"] != "/orders/_NUMBER_" {
		t.Fatalf("unexpected configurable labels: %#v", got)
	}
	if got := conn.getMeasurementBuckets("missing"); len(got) != 2 || got[0] != 100 {
		t.Fatalf("unexpected default buckets: %#v", got)
	}
	if caps := conn.Capabilities(); caps.MutatesData {
		t.Fatal("connector should not mutate data")
	}

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()

	sessionSpan := ss.Spans().AppendEmpty()
	sessionSpan.Attributes().PutInt("sentry.envelop.type.int", 3)
	sessionSpan.Attributes().PutStr("session.status", "exited")
	sessionSpan.Attributes().PutStr("service.name", "frontend")

	eventSpan := ss.Spans().AppendEmpty()
	eventSpan.Attributes().PutInt("sentry.envelop.type.int", 2)
	eventSpan.Attributes().PutStr("level", "error")

	transactionSpan := ss.Spans().AppendEmpty()
	transactionSpan.Attributes().PutInt("sentry.envelop.type.int", 1)
	transactionSpan.Attributes().PutStr("service.name", "frontend")
	transactionSpan.Attributes().PutStr("transaction_path", "/orders/_NUMBER_")
	measurements := transactionSpan.Attributes().PutEmptyMap("measurements")
	fp := measurements.PutEmptyMap("fp")
	fp.PutDouble("value", 12)
	fp.PutStr("unit", "millisecond")
	transactionSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, 0)))
	transactionSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(0, 500*int64(time.Millisecond))))

	if err := conn.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatalf("unexpected consume error: %v", err)
	}

	rm := sink.metrics.ResourceMetrics()
	if rm.Len() != 1 {
		t.Fatalf("expected one resource metric set, got %d", rm.Len())
	}
	metrics := rm.At(0).ScopeMetrics().At(0).Metrics()
	if metrics.Len() != 3 {
		t.Fatalf("expected 3 metrics, got %d", metrics.Len())
	}
	if metrics.At(0).Name() != "sentry_session_exited_count" {
		t.Fatalf("unexpected session metric name: %q", metrics.At(0).Name())
	}
	if metrics.At(1).Name() != "sentry_event_count" {
		t.Fatalf("unexpected event metric name: %q", metrics.At(1).Name())
	}
	if metrics.At(2).Name() != "sentry_measurements_statistic" {
		t.Fatalf("unexpected measurements metric name: %q", metrics.At(2).Name())
	}
}

func TestNormalizeUnit(t *testing.T) {
	cases := map[string]float64{
		"":            2,
		"percent":     0.02,
		"microsecond": 0.002,
		"second":      2000,
		"minute":      120000,
		"hour":        7200000,
		"day":         172800000,
		"week":        1209600000,
		"bit":         0.25,
		"megabyte":    2000000,
		"gibibyte":    2 * 1024 * 1024 * 1024,
		"unknown":     2,
	}
	for unit, want := range cases {
		if got := normalizeUnit(2, unit); got != want {
			t.Fatalf("normalizeUnit(%q) = %v, want %v", unit, got, want)
		}
	}
}
