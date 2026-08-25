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

package logtcpexporter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Netcracker/qubership-open-telemetry-collector/common/graylog"
	"github.com/Netcracker/qubership-open-telemetry-collector/exporter/logtcpexporter/atl/atlmarshaller"
	"github.com/Netcracker/qubership-open-telemetry-collector/utils"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type logTcpExporter struct {
	url                string
	graylogSender      *graylog.GraylogSender
	settings           exporter.Settings
	logger             *zap.Logger
	alLogger           *zap.Logger
	config             *Config
	traceFilterEnabled bool
	spanFilterEnabled  bool
}

func createLogTcpExporter(cfg *Config, settings exporter.Settings) *logTcpExporter {
	logger := settings.Logger.Named("logtcpexporter")
	return &logTcpExporter{
		url:                strings.Trim(cfg.Endpoint, " /"),
		settings:           settings,
		logger:             logger,
		alLogger:           logger.Named("arbitrarylogging"),
		config:             cfg,
		traceFilterEnabled: len(cfg.ATLCfg.TraceFilters) > 0,
		spanFilterEnabled:  len(cfg.ATLCfg.SpanFilters) > 0,
	}
}

func (lte *logTcpExporter) start(_ context.Context, host component.Host) (err error) {
	var address string
	var port uint64
	endpointSplitted := strings.Split(lte.url, ":")
	if len(endpointSplitted) == 1 {
		address = endpointSplitted[0]
		port = 12201
	} else if len(endpointSplitted) > 1 {
		address = endpointSplitted[0]
		port, err = strconv.ParseUint(endpointSplitted[1], 10, 32)
		if err != nil || port < 1 || port > 65535 {
			errMsg := fmt.Sprintf("invalid port in endpoint: %v : %+v", endpointSplitted[1], err)
			lte.logger.Error("Invalid port in endpoint",
				zap.String("port", endpointSplitted[1]),
				zap.Error(err))
			return fmt.Errorf("%s", errMsg)
		}
	}
	freezeTime, err := time.ParseDuration(lte.config.SuccessiveSendErrFreezeTime)
	if err != nil {
		errMsg := fmt.Sprintf("lte.config.successiveSendErrFreezeTime is not parsable : %+v", err)
		lte.logger.Error("Cannot parse successiveSendErrFreezeTime",
			zap.String("value", lte.config.SuccessiveSendErrFreezeTime),
			zap.Error(err))
		return fmt.Errorf("%s", errMsg)
	}
	lte.graylogSender = graylog.NewGraylogSender(
		graylog.Endpoint{
			Transport: graylog.TCP,
			Address:   address,
			Port:      uint(port),
		},
		lte.logger,
		lte.config.ConnPoolSize,
		lte.config.QueueSize,
		lte.config.MaxMessageSendRetryCnt,
		lte.config.MaxSuccessiveSendErrCnt,
		freezeTime,
	)

	return nil
}

func (lte *logTcpExporter) pushTraces(ctx context.Context, traces ptrace.Traces) error {
	isSentry := isSentryTrace(traces)
	lte.logger.Debug("Pushing traces",
		zap.Bool("is_sentry_trace", isSentry),
		zap.Bool("trace_filter_enabled", lte.traceFilterEnabled),
		zap.Bool("span_filter_enabled", lte.spanFilterEnabled))

	if lte.traceFilterEnabled {
		err := lte.sendArbitraryLoggingTrace(traces)
		if err != nil {
			return err
		}
	}

	if !isSentry && !lte.spanFilterEnabled {
		return nil
	}

	rss := traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if isSentry {
					if span.Name() == "Event" {
						if err := lte.sendSentrySpan(span); err != nil {
							lte.logger.Error("Failed to send sentry span", zap.Error(err))
						}
					}
				}
				if lte.spanFilterEnabled {
					if err := lte.sendArbitraryLoggingSpan(span); err != nil {
						lte.logger.Error("Failed to send arbitrary logging span", zap.Error(err))
					}
				}
			}
		}
	}

	return nil
}

func (lte *logTcpExporter) sendArbitraryLoggingTrace(traces ptrace.Traces) error {
	alIndex := lte.getATLTraceFilterIndex(traces)
	if alIndex < 0 {
		lte.alLogger.Debug("Trace is filtered out", zap.Int("al_index", alIndex))
		return nil
	}

	messageBytes, err := atlmarshaller.MarshalTraces(traces)
	if err != nil {
		lte.alLogger.Error("Error marshalling trace", zap.Error(err))
		return err
	}
	traceId := lte.getTraceId(traces)
	extra := map[string]string{
		"trace_id": traceId,
	}

	timestamp, level := lte.getTimestampAndLevel(traces)

	msg := graylog.Message{
		Host:         "open-telemetry-collector",
		ShortMessage: string(messageBytes),
		FullMessage:  "",
		Timestamp:    timestamp.Unix(),
		Level:        level,
		Extra:        extra,
	}
	err = lte.graylogSender.SendToQueue(&msg)
	if err != nil {
		lte.alLogger.Error("Failed to put message on the graylog queue",
			zap.Int64("msg_timestamp", msg.Timestamp),
			zap.Error(err))
		return err
	}
	lte.alLogger.Debug("Message put on the graylog queue",
		zap.Int64("msg_timestamp", msg.Timestamp))

	return nil
}

func (lte *logTcpExporter) sendArbitraryLoggingSpan(span ptrace.Span) error {
	alIndex := lte.getATLSpanFilterIndex(span)
	if alIndex < 0 {
		lte.alLogger.Debug("Span is filtered out", zap.Int("al_index", alIndex))
		return nil
	}
	lte.alLogger.Debug("Arbitrary logging span matched", zap.Int("al_index", alIndex))
	mapping := lte.config.ATLCfg.SpanFilters[alIndex].Mapping

	extra := make(map[string]string)
	var messageStr string
	var hostStr = "open-telemetry-collector"
	var timestamp int64 = 0
	for graylogField, spanFields := range mapping {
		switch graylogField {
		case "__message__":
			messageStr = lte.getStringFromSpanFields(span, spanFields)
		case "__host__":
			hostStr = lte.getStringFromSpanFields(span, spanFields)
		case "__timestamp__":
			timestamp = lte.getTimeFromSpanFields(span, spanFields)
		default:
			extra[graylogField] = lte.getStringFromSpanFields(span, spanFields)
		}
	}

	if timestamp == 0 {
		timestamp = span.EndTimestamp().AsTime().Unix()
	}

	var level uint = 3

	if span.Status().Code().String() != "Error" {
		level = 6
	}

	if len(messageStr) == 0 {
		lte.alLogger.Debug("Span is filtered out because message is empty",
			zap.String("trace_id", span.TraceID().String()),
			zap.String("span_id", span.SpanID().String()))
		return nil
	}

	msg := graylog.Message{
		Host:         hostStr,
		ShortMessage: messageStr,
		FullMessage:  "",
		Timestamp:    timestamp,
		Level:        level,
		Extra:        extra,
	}
	err := lte.graylogSender.SendToQueue(&msg)
	if err != nil {
		lte.alLogger.Error("Failed to put message on the graylog queue",
			zap.Int64("msg_timestamp", msg.Timestamp),
			zap.Error(err))
		return err
	}
	lte.alLogger.Debug("Message put on the graylog queue",
		zap.Int64("msg_timestamp", msg.Timestamp))

	return nil
}

func isSentryTrace(traces ptrace.Traces) bool {
	rss := traces.ResourceSpans()
	if rss.Len() < 1 {
		return false
	}
	val, ok := rss.At(0).Resource().Attributes().Get("trace.source.type")
	if ok {
		return val.AsString() == "sentry"
	}
	return false
}

func (lte *logTcpExporter) getTraceId(traces ptrace.Traces) string {
	rss := traces.ResourceSpans()
	if rss.Len() < 1 {
		return ""
	}
	sss := rss.At(0).ScopeSpans()
	if sss.Len() < 1 {
		return ""
	}
	spans := sss.At(0).Spans()
	if spans.Len() < 1 {
		return ""
	}
	return spans.At(0).TraceID().String()
}

func (lte *logTcpExporter) getTimestampAndLevel(traces ptrace.Traces) (time.Time, uint) {
	result := time.Time{}
	var level uint = 6
	rss := traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				endTimestamp := span.EndTimestamp().AsTime()
				if endTimestamp.After(result) {
					result = endTimestamp
				}
				if level == 6 && span.Status().Code().String() == "Error" {
					level = 3
				}
			}
		}
	}
	if result.IsZero() {
		return time.Now(), level
	}

	return result, level
}

func (lte *logTcpExporter) getATLSpanFilterIndex(span ptrace.Span) int {
	spanFilters := lte.config.ATLCfg.SpanFilters

	for filterIndex, filter := range spanFilters {
		if lte.checkSpanFilterCondition(span, filter) {
			lte.alLogger.Debug("Span filter evaluated",
				zap.Int("span_filter_index", filterIndex),
				zap.Bool("matched", true))
			return filterIndex
		} else {
			lte.alLogger.Debug("Span filter evaluated",
				zap.Int("span_filter_index", filterIndex),
				zap.Bool("matched", false))
		}
	}

	return -1
}

func (lte *logTcpExporter) checkSpanFilterCondition(span ptrace.Span, filter ATLFilter) bool {
	if len(filter.ServiceNames) > 0 {
		serviceName, ok := span.Attributes().Get("service.name")
		if !ok {
			return false
		}
		if utils.FindStringIndexInArray(filter.ServiceNames, serviceName.AsString()) < 0 {
			return false
		}
	}

	for k, v := range filter.Tags {
		tag, ok := span.Attributes().Get(k)
		if !ok {
			return false
		}
		if tag.AsString() != v {
			return false
		}
	}

	return true
}

func (lte *logTcpExporter) checkResourceFilterCondition(resource pcommon.Resource, filter ATLFilter) bool {
	if len(filter.ServiceNames) > 0 {
		return false
	}

	for k, v := range filter.Tags {
		tag, ok := resource.Attributes().Get(k)
		if !ok {
			return false
		}
		if tag.AsString() != v {
			return false
		}
	}

	return true
}

func (lte *logTcpExporter) getATLTraceFilterIndex(traces ptrace.Traces) int {
	traceFilters := lte.config.ATLCfg.TraceFilters
	for filterIndex, filter := range traceFilters {
		if lte.checkTraceFilterCondition(traces, filter) {
			lte.alLogger.Debug("Trace filter evaluated",
				zap.Int("trace_filter_index", filterIndex),
				zap.Bool("matched", true))
			return filterIndex
		} else {
			lte.alLogger.Debug("Trace filter evaluated",
				zap.Int("trace_filter_index", filterIndex),
				zap.Bool("matched", false))
		}
	}

	return -1
}

func (lte *logTcpExporter) checkTraceFilterCondition(traces ptrace.Traces, filter ATLFilter) bool {
	rss := traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		if lte.checkResourceFilterCondition(rss.At(i).Resource(), filter) {
			return true
		}
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				if lte.checkSpanFilterCondition(spans.At(k), filter) {
					return true
				}
			}
		}
	}

	return false
}

func (lte *logTcpExporter) getStringFromSpanFields(span ptrace.Span, spanFields []string) string {
	result := make([]string, len(spanFields))
	empty := true
	for i, spanField := range spanFields {
		switch spanField {
		case "__spanId__":
			result[i] = span.SpanID().String()
		case "__traceId__":
			result[i] = span.TraceID().String()
		case "__name__":
			result[i] = span.Name()
		case "__end_timestamp__":
			result[i] = span.EndTimestamp().String()
		case "__start_timestamp__":
			result[i] = span.StartTimestamp().String()
		case "__kind__":
			result[i] = span.Kind().String()
		case "__parentSpanId__":
			result[i] = span.ParentSpanID().String()
		default:
			value, ok := span.Attributes().Get(spanField)
			if ok {
				result[i] = value.AsString()
			} else {
				result[i] = ""
			}
		}
		if result[i] != "" {
			empty = false
		}
	}
	if empty {
		return ""
	}

	return strings.Join(result, "\n")
}

func (lte *logTcpExporter) getTimeFromSpanFields(span ptrace.Span, spanFields []string) int64 {
	if len(spanFields) == 0 {
		return 0
	}
	switch spanFields[0] {
	case "__startTime__":
		return span.StartTimestamp().AsTime().Unix()
	case "__endTime__":
		return span.EndTimestamp().AsTime().Unix()
	}

	return 0
}

func (lte *logTcpExporter) sendSentrySpan(span ptrace.Span) error {
	var spanIdStr, traceIdStr, levelStr, sdkStr, messageStr, fullMessageStr, eventIdStr, versionStr, nameStr, platformStr, userIdStr, transactionStr, categoryStr, urlStr, browserStr string
	var graylogLevel uint
	var timestampUnix int64

	spanId, ok := span.Attributes().Get("contexts.trace.span_id")
	if ok {
		spanIdStr = spanId.AsString()
	}

	traceId, ok := span.Attributes().Get("contexts.trace.trace_id")
	if ok {
		traceIdStr = traceId.AsString()
	}

	level, ok := span.Attributes().Get("level")
	if ok {
		levelStr = level.AsString()
		graylogLevel = lte.getGraylogLevel(levelStr)
	}

	sdk, ok := span.Attributes().Get("sdk")
	if ok {
		sdkStr = sdk.AsString()
	}

	exceptionValues, ok := span.Attributes().Get("exception.values")
	if ok {
		fullMessageStr = exceptionValues.AsString()
	}

	message, ok := span.Attributes().Get("message")
	if ok {
		messageStr = message.AsString()
	}
	if messageStr == "" {
		message, ok = span.Attributes().Get("context.error")
		if ok {
			messageStr = message.AsString()
		}
	}
	if messageStr == "" {
		messageStr = fullMessageStr
	}
	if messageStr == "" {
		messageStr = "empty_message"
	}

	timestamp, ok := span.Attributes().Get("timestamp")
	if ok {
		timestampUnix = int64(timestamp.Double())
	}

	eventId, ok := span.Attributes().Get("event_id")
	if ok {
		eventIdStr = eventId.AsString()
	}

	version, ok := span.Attributes().Get("version")
	if ok {
		versionStr = version.AsString()
	}
	if versionStr == "" {
		versionStr = "empty_version"
	}

	name, ok := span.Attributes().Get("name")
	if ok {
		nameStr = name.AsString()
	}

	platform, ok := span.Attributes().Get("platform")
	if ok {
		platformStr = platform.AsString()
	}

	userId, ok := span.Attributes().Get("user_id")
	if ok {
		userIdStr = userId.AsString()
	}

	transaction, ok := span.Attributes().Get("tags.transaction")
	if ok {
		transactionStr = transaction.AsString()
	}

	category, ok := span.Attributes().Get("category")
	if ok {
		categoryStr = category.AsString()
	}

	url, ok := span.Attributes().Get("url")
	if ok {
		urlStr = url.AsString()
	}

	browser, ok := span.Attributes().Get("browser")
	if ok {
		browserStr = browser.AsString()
	}

	timestampParsed := time.Unix(timestampUnix, 0)
	msg := graylog.Message{
		Version:      versionStr,
		Host:         "user_browser",
		ShortMessage: messageStr,
		FullMessage:  fullMessageStr,
		Timestamp:    timestampUnix,
		Level:        graylogLevel,
		Extra: map[string]string{
			"span_id":     spanIdStr,
			"trace_id":    traceIdStr,
			"component":   "frontend",
			"facility":    "open-telemetry-collector",
			"sdk":         sdkStr,
			"stacktrace":  fullMessageStr,
			"event_id":    eventIdStr,
			"name":        nameStr,
			"platform":    platformStr,
			"time":        timestampParsed.Format(time.RFC3339),
			"user_id":     userIdStr,
			"transaction": transactionStr,
			"category":    categoryStr,
			"url":         urlStr,
			"browser":     browserStr,
		},
	}
	err := lte.graylogSender.SendToQueue(&msg)
	if err != nil {
		lte.logger.Error("Failed to put message on the graylog queue",
			zap.String("trace_id", traceIdStr),
			zap.String("span_id", spanIdStr),
			zap.Error(err))
		return err
	}
	lte.logger.Debug("Message put on the graylog queue",
		zap.String("trace_id", traceIdStr),
		zap.String("span_id", spanIdStr))

	if graylogLevel == 3 {
		breadcrumbs, ok := span.Attributes().Get("breadcrumbs")
		if !ok {
			return nil
		}
		breadcrumbsList := breadcrumbs.Slice().AsRaw()
		for i, breadcrumb := range breadcrumbsList {
			breadcrumbMap, ok := breadcrumb.(map[string]interface{})
			if !ok {
				lte.logger.Error("Type assertion error",
					zap.String("actual_type", fmt.Sprintf("%T", breadcrumb)))
			}
			levelB, _ := breadcrumbMap["level"].(string)
			timestampB, _ := breadcrumbMap["timestamp"].(float64)
			timestampUnixB := int64(timestampB)
			categoryB, _ := breadcrumbMap["category"].(string)
			messageB, _ := breadcrumbMap["message"].(string)
			statusB, _ := breadcrumbMap["status"].(string)

			extra := map[string]string{
				"span_id":     spanIdStr,
				"trace_id":    traceIdStr,
				"component":   "frontend",
				"facility":    "open-telemetry-collector",
				"sdk":         sdkStr,
				"stacktrace":  fullMessageStr,
				"event_id":    eventIdStr,
				"name":        nameStr,
				"platform":    platformStr,
				"time":        timestampParsed.Format(time.RFC3339),
				"user_id":     userIdStr,
				"transaction": transactionStr,
				"category":    getFirst(categoryB, categoryStr),
				"url":         urlStr,
				"browser":     browserStr,
			}

			if statusB != "" {
				extra["status"] = statusB
			}

			msg := graylog.Message{
				Version:      versionStr,
				Host:         "user_browser",
				ShortMessage: getFirst(messageB, messageStr),
				FullMessage:  fullMessageStr,
				Timestamp:    getFirstInt64(timestampUnixB, timestampUnix),
				Level:        lte.getGraylogLevel(getFirst(levelB, levelStr)),
				Extra:        extra,
			}
			err := lte.graylogSender.SendToQueue(&msg)
			if err != nil {
				lte.logger.Error("Failed to put breadcrumb message on the graylog queue",
					zap.String("trace_id", traceIdStr),
					zap.String("span_id", spanIdStr),
					zap.Int("breadcrumb_index", i),
					zap.Error(err))
				return err
			}
			lte.logger.Debug("Breadcrumb message put on the graylog queue",
				zap.String("trace_id", traceIdStr),
				zap.String("span_id", spanIdStr),
				zap.Int("breadcrumb_index", i))
		}
	}
	return nil
}

func getFirst(strings ...string) string {
	for _, str := range strings {
		if str != "" {
			return str
		}
	}
	return ""
}

func getFirstInt64(ints ...int64) int64 {
	for _, i := range ints {
		if i != 0 {
			return i
		}
	}
	return 0
}

func (lte *logTcpExporter) getGraylogLevel(level string) uint {
	switch strings.ToLower(level) {
	case "fatal":
		return 0
	case "error":
		return 3
	case "warning":
		return 4
	case "log":
		return 5
	case "info":
		return 6
	case "debug":
		return 7
	}
	lte.logger.Error("Unknown logging level received from Sentry, falling back to Graylog level 3",
		zap.String("sentry_level", level),
		zap.Uint("graylog_level", 3))
	return 3
}
