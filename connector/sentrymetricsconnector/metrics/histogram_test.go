package metrics

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

func TestCustomHistogramObserveAndUpdate(t *testing.T) {
	h := NewCustomHistogram(zap.NewNop())
	h.ObserveSingle(5, []float64{10, 20}, map[string]string{"type": "fp"})
	h.ObserveSingle(15, []float64{10, 20}, map[string]string{"type": "fp"})

	metric := pmetric.NewMetric()
	h.UpdateDataPoints(metric)

	if metric.Name() != "sentry_measurements_statistic" {
		t.Fatalf("unexpected metric name: %q", metric.Name())
	}
	dp := metric.Histogram().DataPoints().At(0)
	if dp.Count() != 2 {
		t.Fatalf("unexpected count: %d", dp.Count())
	}
	if dp.Sum() != 20 {
		t.Fatalf("unexpected sum: %v", dp.Sum())
	}
	if got, _ := dp.Attributes().Get("type"); got.AsString() != "fp" {
		t.Fatalf("unexpected label value: %v", got.AsString())
	}
}
