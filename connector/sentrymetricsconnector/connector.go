// Copyright 2025 Qubership
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sentrymetricsconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"

	"github.com/Netcracker/qubership-open-telemetry-collector/connector/sentrymetricsconnector/metrics"
	"github.com/Netcracker/qubership-open-telemetry-collector/receiver/sentryreceiver/models"

	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"go.uber.org/zap"
)

const scopeName = "otelcol/sentrymetricsconnector"

// sentrymetrics can evaluate metrics based on sentry traces
// and emit them to a metrics pipeline.
type sentrymetrics struct {
	config          *Config
	metricsConsumer consumer.Metrics
	component.StartFunc
	component.ShutdownFunc
	logger                     *zap.Logger
	counter                    int64
	measurementsHist           *metrics.CustomHistogram
	measurementsBuckets        map[string][]float64
	defaultMeasurementsBuckets []float64
	measurementsLabels         map[string]map[string]string
	defaultMeasurementsLabels  map[string]string
}

func CreateSentryMetricsConnector(config *Config, metricsConsumer consumer.Metrics, set connector.Settings) *sentrymetrics {
	result := sentrymetrics{}
	result.config = config
	result.metricsConsumer = metricsConsumer
	result.logger = set.Logger
	result.measurementsHist = metrics.NewCustomHistogram(set.Logger)
	result.defaultMeasurementsBuckets = config.SentryMeasurementsCfg.DefaultBuckets
	result.measurementsBuckets = make(map[string][]float64)
	for k, v := range config.SentryMeasurementsCfg.Custom {
		result.measurementsBuckets[k] = v.Buckets
	}
	result.defaultMeasurementsLabels = config.SentryMeasurementsCfg.DefaultLabels
	result.logger.Sugar().Infof("DefaultMeasurementsLabels %+v", result.defaultMeasurementsLabels)
	result.measurementsLabels = make(map[string]map[string]string)
	for k, v := range config.SentryMeasurementsCfg.Custom {
		if v.Labels != nil {
			result.measurementsLabels[k] = *v.Labels
			result.logger.Sugar().Infof("Custom measurementsLabels for %v : %+v", k, result.measurementsLabels[k])
		}
	}
	return &result
}

func (c *sentrymetrics) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *sentrymetrics) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	countMetrics := pmetric.NewMetrics()
	resourceMetric := countMetrics.ResourceMetrics().AppendEmpty()
	scopeMetrics := resourceMetric.ScopeMetrics().AppendEmpty()
	c.calculateSessionCountMetric(scopeMetrics.Metrics().AppendEmpty(), td)
	c.calculateEventCountMetric(scopeMetrics.Metrics().AppendEmpty(), td)
	c.calculateMeasurementsMetric(scopeMetrics.Metrics().AppendEmpty(), td)
	return c.metricsConsumer.ConsumeMetrics(ctx, countMetrics)
}

func (c *sentrymetrics) calculateSessionCountMetric(metric pmetric.Metric, td ptrace.Traces) error {
	metric.SetName("sentry_session_exited_count")
	metric.SetDescription("The metric counts total number of sessions")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(1)
	sum.SetIsMonotonic(true)
	dataPoints := sum.DataPoints()

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		ilss := rs.ScopeSpans()
		for j := 0; j < ilss.Len(); j++ {
			spans := ilss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				var envelopTypeInt int64
				var sessionStatusStr string
				envelopType, ok := span.Attributes().Get("sentry.envelop.type.int")
				if ok {
					envelopTypeInt = envelopType.Int()
				}
				if envelopTypeInt != models.ENVELOP_TYPE_SESSION {
					continue
				}
				sessionStatus, ok := span.Attributes().Get("session.status")
				if ok {
					sessionStatusStr = sessionStatus.AsString()
				}
				if sessionStatusStr != "exited" {
					continue
				}
				var serviceNameStr string
				serviceName, ok := span.Attributes().Get("service.name")
				if ok {
					serviceNameStr = serviceName.AsString()
				}
				dataPoint := dataPoints.AppendEmpty()
				dataPoint.Attributes().PutStr("service_name", serviceNameStr)
				dataPoint.SetDoubleValue(1.0)
			}
		}
	}

	return nil
}

func (c *sentrymetrics) calculateEventCountMetric(metric pmetric.Metric, td ptrace.Traces) error {
	metric.SetName("sentry_event_count")
	metric.SetDescription("The metric counts total number of events by level")
	sum := metric.SetEmptySum()
	sum.SetAggregationTemporality(1)
	sum.SetIsMonotonic(true)
	dataPoints := sum.DataPoints()
	labelsToExtract := c.config.SentryEventCountCfg.Labels

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		ilss := rs.ScopeSpans()
		for j := 0; j < ilss.Len(); j++ {
			spans := ilss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				var envelopTypeInt int64
				envelopType, ok := span.Attributes().Get("sentry.envelop.type.int")
				if ok {
					envelopTypeInt = envelopType.Int()
				}
				if envelopTypeInt != models.ENVELOP_TYPE_EVENT {
					continue
				}
				dataPoint := dataPoints.AppendEmpty()
				dataPoint.SetDoubleValue(1.0)
				labels := c.getLabels(span, labelsToExtract)
				for labelName, labelValue := range labels {
					dataPoint.Attributes().PutStr(labelName, labelValue)
				}
			}
		}
	}

	return nil
}

func (c *sentrymetrics) calculateMeasurementsMetric(metric pmetric.Metric, td ptrace.Traces) error {
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		ilss := rs.ScopeSpans()
		for j := 0; j < ilss.Len(); j++ {
			spans := ilss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				var envelopTypeInt int64
				envelopType, ok := span.Attributes().Get("sentry.envelop.type.int")
				if ok {
					envelopTypeInt = envelopType.Int()
				}
				if envelopTypeInt != models.ENVELOP_TYPE_TRANSACTION {
					continue
				}
				var measurementsCommonMap pcommon.Map
				measurements, ok := span.Attributes().Get("measurements")
				if ok {
					measurementsCommonMap = measurements.Map()
				}
				configurableLabels := c.getConfigurableMeasurementLabels(span, "")
				c.logger.Sugar().Debugf("SentryMetricsConnector : GOT TRANSACTION with measurements size=%v, configurableLabels=%+v", measurementsCommonMap.Len(), configurableLabels)
				measurementsCommonMap.Range(func(k string, v pcommon.Value) bool {
					labels := make(map[string]string)
					labels["type"] = k
					if c.measurementsLabels[k] != nil {
						customConfigurableLabels := c.getConfigurableMeasurementLabels(span, k)
						for k, v := range customConfigurableLabels {
							labels[k] = v
						}
					} else {
						for k, v := range configurableLabels {
							labels[k] = v
						}
					}
					var measurementFloat float64
					var unitStr string
					val, okVal := v.Map().Get("value")
					if okVal {
						measurementFloat = val.Double()
					}
					unit, okUnit := v.Map().Get("unit")
					if okUnit {
						unitStr = unit.AsString()
					}

					c.logger.Sugar().Debugf("SentryMetricsConnector : Measurements datapoint : labels=%+v, value=%v, unitStr=%v", labels, measurementFloat, unitStr)
					if okVal {
						c.measurementsHist.ObserveSingle(normalizeUnit(measurementFloat, unitStr), c.getMeasurementBuckets(k), labels)
					} else {
						c.logger.Error("SentryMetricsConnector : Error reading measurements value")
					}
					return true
				})
				labels := make(map[string]string)
				labels["type"] = "transaction_duration"
				if c.measurementsLabels["transaction_duration"] != nil {
					customConfigurableLabels := c.getConfigurableMeasurementLabels(span, "transaction_duration")
					for k, v := range customConfigurableLabels {
						labels[k] = v
					}
				} else {
					for k, v := range configurableLabels {
						labels[k] = v
					}
				}
				var unitStr string
				durationFloat := float64(span.EndTimestamp().AsTime().Sub(span.StartTimestamp().AsTime()).Milliseconds())
				c.logger.Sugar().Debugf("SentryMetricsConnector : Measurements datapoint : labels=%+v, value=%v, unitStr=%v", labels, durationFloat, unitStr)
				c.measurementsHist.ObserveSingle(normalizeUnit(durationFloat, unitStr), c.getMeasurementBuckets("transaction_duration"), labels)
			}
		}
	}
	c.measurementsHist.UpdateDataPoints(metric)

	return nil
}

func (c *sentrymetrics) getConfigurableMeasurementLabels(span ptrace.Span, measurementType string) map[string]string {
	var labelsToExtract map[string]string
	if measurementType == "" {
		labelsToExtract = c.defaultMeasurementsLabels
	} else {
		labelsToExtract = c.measurementsLabels[measurementType]
	}
	if labelsToExtract == nil {
		labelsToExtract = c.defaultMeasurementsLabels
		c.measurementsLabels[measurementType] = labelsToExtract
		c.logger.Sugar().Debugf("Set default labelsToExtract %+v for measurement %v", labelsToExtract, measurementType)
	}

	return c.getLabels(span, labelsToExtract)
}

func (c *sentrymetrics) getLabels(span ptrace.Span, labelsToExtract map[string]string) map[string]string {
	result := make(map[string]string)
	for labelName, labelPath := range labelsToExtract {
		labelValue, ok := span.Attributes().Get(labelPath)
		if ok {
			result[labelName] = labelValue.AsString()
		} else {
			result[labelName] = ""
		}
	}

	return result
}

func (c *sentrymetrics) getMeasurementBuckets(measurementType string) []float64 {
	buckets := c.measurementsBuckets[measurementType]
	if len(buckets) == 0 {
		buckets = c.defaultMeasurementsBuckets
		c.measurementsBuckets[measurementType] = buckets
		c.logger.Sugar().Debugf("Set default buckets %+v for measurement %v", buckets, measurementType)
	}
	return buckets
}

func normalizeUnit(val float64, unit string) float64 {
	switch unit {
	case "millisecond", "byte", "none", "ratio", "":
		return val
	case "percent":
		return val / 100
	case "microsecond":
		return val / 1000
	case "nanosecond":
		return val / 1000_000
	case "second", "kilobyte":
		return val * 1000
	case "minute":
		return val * 1000 * 60
	case "hour":
		return val * 1000 * 60 * 60
	case "day":
		return val * 1000 * 60 * 60 * 24
	case "week":
		return val * 1000 * 60 * 60 * 24 * 7
	case "bit":
		return val / 8
	case "megabyte":
		return val * 1000_000
	case "gigabyte":
		return val * 1000_000_000
	case "terabyte":
		return val * 1000_000_000_000
	case "petabyte":
		return val * 1000_000_000_000_000
	case "exabyte":
		return val * 1000_000_000_000_000_000
	case "kibibyte":
		return val * 1024
	case "mebibyte":
		return val * 1024 * 1024
	case "gibibyte":
		return val * 1024 * 1024 * 1024
	case "tebibyte":
		return val * 1024 * 1024 * 1024 * 1024
	case "pebibyte":
		return val * 1024 * 1024 * 1024 * 1024 * 1024
	case "exbibyte":
		return val * 1024 * 1024 * 1024 * 1024 * 1024 * 1024
	}
	return val
}
