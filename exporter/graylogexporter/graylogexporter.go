// Copyright 2024 Qubership
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

package graylogexporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Netcracker/qubership-open-telemetry-collector/common/graylog"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const (
	defaultGraylogPort    = 12201
	defaultNoMessage      = "No message provided"
	defaultNoShortMessage = "No short message provided"
	defaultGELFVersion    = "1.1"
)

var severityTextToLevel = map[string]uint{
	"emergency": 0, "panic": 0,
	"alert":    1,
	"critical": 2, "crit": 2,
	"error": 3, "err": 3,
	"warning": 4, "warn": 4,
	"notice": 5,
	"info":   6,
	"debug":  7, "trace": 7,
}

type grayLogExporter struct {
	url           string
	graylogSender *graylog.GraylogSender
	settings      exporter.Settings
	logger        *zap.Logger
	config        *Config
}

func createLogExporter(cfg *Config, settings exporter.Settings) *grayLogExporter {
	return &grayLogExporter{
		url:      strings.Trim(cfg.Endpoint, " /"),
		settings: settings,
		logger:   settings.Logger,
		config:   cfg,
	}
}

func (le *grayLogExporter) start(_ context.Context, _ component.Host) error {
	address, port, err := parseEndpoint(le.url)
	if err != nil {
		le.logger.Error("Invalid endpoint", zap.Error(err))
		return err
	}

	freezeTime, err := time.ParseDuration(le.config.SuccessiveSendErrFreezeTime)
	if err != nil {
		return fmt.Errorf("invalid freeze duration: %w", err)
	}

	le.graylogSender = graylog.NewGraylogSender(
		graylog.Endpoint{
			Transport: graylog.TCP,
			Address:   address,
			Port:      uint(port),
		},
		le.logger,
		le.config.ConnPoolSize,
		le.config.QueueSize,
		le.config.MaxMessageSendRetryCnt,
		le.config.MaxSuccessiveSendErrCnt,
		freezeTime,
	)
	return nil
}

func parseEndpoint(endpoint string) (string, uint, error) {
	parts := strings.Split(endpoint, ":")
	if len(parts) == 1 {
		return parts[0], defaultGraylogPort, nil
	}
	port, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in endpoint: %w", err)
	}
	return parts[0], uint(port), nil
}

func (le *grayLogExporter) pushLogs(ctx context.Context, logs plog.Logs) error {
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLog := logs.ResourceLogs().At(i)
		resource := resourceLog.Resource()
		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)
			for k := 0; k < scopeLog.LogRecords().Len(); k++ {
				logRecord := scopeLog.LogRecords().At(k)
				msg, err := le.logRecordToMessage(logRecord, resource.Attributes())
				if err != nil {
					le.logger.Error("Failed to convert log to Graylog message", zap.Error(err))
					continue
				}
				if err := le.graylogSender.SendToQueue(msg); err != nil {
					le.logger.Warn("Failed to enqueue message", zap.Error(err))
				}
			}
		}
	}
	return nil
}

func (le *grayLogExporter) logRecordToMessage(logRecord plog.LogRecord, resourceAttrs pcommon.Map) (*graylog.Message, error) {
	timestamp, level := le.getTimestampAndLevel(logRecord)
	attributes, message, _ := extractAttributes(logRecord.Body())

	extra := mergeAttributes(attributes)
	mergeMapAttributes(extra, "attr.", logRecord.Attributes())
	mergeMapAttributes(extra, "resource.", resourceAttrs)

	fullmsg := defaultIfEmpty(
		le.getMappedValue(le.config.GELFMapping.FullMessage, attributes, logRecord.Attributes()),
		message,
	)
	shortmsg := defaultIfEmpty(
		le.getMappedValue(le.config.GELFMapping.ShortMessage, attributes, logRecord.Attributes()),
		message,
	)
	hostname := le.getMappedValue(le.config.GELFMapping.Host, attributes, logRecord.Attributes())

	return &graylog.Message{
		Version:      le.config.GELFMapping.Version,
		Host:         hostname,
		ShortMessage: shortmsg,
		FullMessage:  fullmsg,
		Timestamp:    timestamp.Unix(),
		Level:        level,
		Extra:        extra,
	}, nil
}

func extractAttributes(body pcommon.Value) (map[string]interface{}, string, error) {
	attributes := make(map[string]interface{})
	switch body.Type() {
	case pcommon.ValueTypeStr:
		msg := body.AsString()
		if msg == "" {
			return nil, defaultNoMessage, nil
		}
		if decoded, err := decodeConcatenatedJSON(msg); err == nil {
			if m, ok := decoded["message"].(string); ok {
				msg = m
			}
			return decoded, msg, nil
		}
		return nil, msg, nil
	case pcommon.ValueTypeMap:
		body.Map().Range(func(k string, v pcommon.Value) bool {
			attributes[k] = v.AsString()
			return true
		})
		if msg, ok := attributes["message"].(string); ok {
			return attributes, msg, nil
		}
		return attributes, defaultNoMessage, nil
	case pcommon.ValueTypeBytes:
		attributes["bytes"] = body.Bytes().AsRaw()
		return attributes, defaultNoMessage, nil
	default:
		return nil, defaultNoMessage, nil
	}
}

func decodeConcatenatedJSON(input string) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	decoder := json.NewDecoder(strings.NewReader(input))
	for {
		var obj map[string]interface{}
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		for k, v := range obj {
			result[k] = v
		}
	}
	return result, nil
}

func getStringFromPcommonValue(val pcommon.Value) (string, bool) {
	switch val.Type() {
	case pcommon.ValueTypeStr:
		return val.AsString(), true
	case pcommon.ValueTypeBool:
		return strconv.FormatBool(val.Bool()), true
	case pcommon.ValueTypeInt:
		return strconv.FormatInt(val.Int(), 10), true
	case pcommon.ValueTypeDouble:
		return fmt.Sprintf("%f", val.Double()), true
	case pcommon.ValueTypeBytes:
		return string(val.Bytes().AsRaw()), true
	default:
		return "", false
	}
}

func mergeAttributes(src map[string]interface{}) map[string]string {
	out := make(map[string]string)
	for k, v := range src {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

func mergeMapAttributes(dest map[string]string, prefix string, attrs pcommon.Map) {
	attrs.Range(func(k string, v pcommon.Value) bool {
		if str, ok := getStringFromPcommonValue(v); ok {
			dest[prefix+k] = str
		}
		return true
	})
}

func (le *grayLogExporter) getMappedValue(key string, attributes map[string]interface{}, logAttrs pcommon.Map) string {
	if key == "" {
		return "empty key"
	}
	if val, ok := attributes[key]; ok {
		return fmt.Sprintf("%v", val)
	}
	if val, ok := logAttrs.Get(key); ok {
		if s, ok := getStringFromPcommonValue(val); ok {
			return s
		}
	}
	return fmt.Sprintf("%v not found", key)
}

func defaultIfEmpty(val, fallback string) string {
	val = strings.TrimSpace(val)
	if val == "" || strings.Contains(strings.ToLower(val), "not found") {
		return fallback
	}
	return val
}

func (le *grayLogExporter) getTimestampAndLevel(logRecord plog.LogRecord) (time.Time, uint) {
	timestamp := logRecord.Timestamp().AsTime()
	if logRecord.Timestamp() == 0 {
		timestamp = time.Now()
		le.logger.Warn("Log record missing timestamp, using current time")
	}
	text := strings.ToLower(logRecord.SeverityText())
	if level, found := severityTextToLevel[text]; found {
		return timestamp, level
	}
	severity := logRecord.SeverityNumber()
	switch {
	case severity >= plog.SeverityNumberFatal && severity <= plog.SeverityNumberFatal4:
		return timestamp, 2
	case severity >= plog.SeverityNumberError && severity <= plog.SeverityNumberError4:
		return timestamp, 3
	case severity >= plog.SeverityNumberWarn && severity <= plog.SeverityNumberWarn4:
		return timestamp, 4
	case severity >= plog.SeverityNumberInfo && severity <= plog.SeverityNumberInfo4:
		return timestamp, 6
	case severity >= plog.SeverityNumberDebug && severity <= plog.SeverityNumberDebug4:
		return timestamp, 7
	default:
		return timestamp, 6
	}
}
