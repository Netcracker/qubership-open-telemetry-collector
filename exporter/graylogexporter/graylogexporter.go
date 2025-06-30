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
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

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
	var address string
	var port uint64
	useBulk := false
	if le.config.BatchSize > 1 {
		useBulk = true
	}

	endpointSplitted := strings.Split(le.url, ":")
	if len(endpointSplitted) == 1 {
		address = endpointSplitted[0]
		port = 12201
	} else {
		address = endpointSplitted[0]
		var err error
		port, err = strconv.ParseUint(endpointSplitted[1], 10, 64)
		if err != nil {
			errMsg := fmt.Sprintf("Error parsing port '%v': %v", endpointSplitted[1], err)
			le.logger.Error(errMsg)
			return fmt.Errorf(errMsg)
		}
	}

	freezeTime, err := time.ParseDuration(le.config.SuccessiveSendErrFreezeTime)
	if err != nil {
		errMsg := fmt.Sprintf("Invalid SuccessiveSendErrFreezeTime '%s': %v", le.config.SuccessiveSendErrFreezeTime, err)
		le.logger.Error(errMsg)
		return fmt.Errorf(errMsg)
	}

	le.graylogSender = graylog.NewGraylogSender(
		graylog.Endpoint{
			Transport: graylog.TCP,
			Address:   address,
			Port:      uint(port),
		},
		le.logger,
		le.config.ConnPoolSize,
		le.config.BatchSize,
		le.config.MaxMessageSendRetryCnt,
		le.config.MaxSuccessiveSendErrCnt,
		freezeTime,
		useBulk,
	)

	return nil
}

func decodeConcatenatedJSON(logbody string) (map[string]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(logbody))
	result := make(map[string]interface{})
	for {
		var obj map[string]interface{}
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("JSON decode error: %w", err)
		}
		for k, v := range obj {
			result[k] = v
		}
	}
	return result, nil
}

func extractAttributes(body pcommon.Value) (map[string]interface{}, string, error) {
	attributes := make(map[string]interface{})
	var message string

	switch body.Type() {
	case pcommon.ValueTypeStr:
		message = body.AsString()
		if message == "" {
			message = "No message provided"
		}
		decoded, err := decodeConcatenatedJSON(message)
		if err == nil {
			attributes = decoded
			if val, ok := attributes["message"]; ok {
				message, _ = val.(string)
			}
			return attributes, message, nil
		}
		return nil, message, nil
	case pcommon.ValueTypeMap:
		body.Map().Range(func(k string, v pcommon.Value) bool {
			attributes[k] = v.AsString()
			return true
		})
		if val, ok := attributes["message"]; ok {
			message, _ = val.(string)
		}
		return attributes, message, nil
	case pcommon.ValueTypeSlice:
		message = body.AsString()
		return nil, message, nil
	case pcommon.ValueTypeBytes:
		attributes["bytes"] = body.Bytes()
		return attributes, "", nil
	case pcommon.ValueTypeEmpty:
		return nil, "", fmt.Errorf("log body is empty")
	default:
		return nil, "", fmt.Errorf("unsupported body type: %v", body.Type())
	}
}

func (le *grayLogExporter) getMappedValue(key string, attributes map[string]interface{}, logAttrs pcommon.Map) string {
	if key == "" {
		return fmt.Sprintf("empty key: %v", key)
	}
	if val, ok := attributes[key]; ok {
		le.logger.Debug("Using attribute", zap.String("key", key), zap.Any("value", val))
		return fmt.Sprintf("%v", val)
	}
	if val, ok := getStringFromPcommonMap(logAttrs, key); ok {
		le.logger.Debug("Using log attribute", zap.String("key", key), zap.Any("value", val))
		return val
	}

	return fmt.Sprintf("%v not found", key)

}

func getStringFromPcommonMap(m pcommon.Map, key string) (string, bool) {
	val, ok := m.Get(key)
	if !ok {
		return "", false
	}
	switch val.Type() {
	case pcommon.ValueTypeStr:
		return val.AsString(), true
	case pcommon.ValueTypeBool:
		return strconv.FormatBool(val.Bool()), true
	case pcommon.ValueTypeInt:
		return strconv.FormatInt(val.Int(), 10), true
	case pcommon.ValueTypeDouble:
		return fmt.Sprintf("%f", val.Double()), true
	default:
		return "", false
	}
}

func (le *grayLogExporter) logRecordToMessage(logRecord plog.LogRecord, resourceAttrs pcommon.Map) (*graylog.Message, error) {
	le.logger.Sugar().Debugf("msg receiveid and ready to parse: %v, %v, %v", logRecord.Body().AsString(), logRecord.Attributes(), resourceAttrs)
	timestamp, level := le.getTimestampAndLevel(logRecord)
	attributes, message, err := extractAttributes(logRecord.Body())
	if err != nil {
		return nil, err
	}

	extra := make(map[string]string)
	for k, v := range attributes {
		extra["_"+k] = cleanAndStringifyAny(v)
	}

	logRecord.Attributes().Range(func(k string, v pcommon.Value) bool {
		extra["_"+k] = cleanAndStringifyOtelValue(v)
		return true
	})
	resourceAttrs.Range(func(k string, v pcommon.Value) bool {
		extra["_resource."+k] = cleanAndStringifyOtelValue(v)
		return true
	})

	fullmsg := le.getMappedValue(le.config.GELFMapping.FullMessage, attributes, logRecord.Attributes())
	if fullmsg == "" || strings.Contains(strings.ToLower(fullmsg), "not found") {
		fullmsg = message
	}
	shortmsg := "No short message provided"
	if le.config.GELFMapping.ShortMessage == "log" || le.config.GELFMapping.ShortMessage == "message" {
		shortmsg = message
	} else {
		shortmsg = le.getMappedValue(le.config.GELFMapping.ShortMessage, attributes, logRecord.Attributes())
	}
	hostname := le.getMappedValue(le.config.GELFMapping.Host, attributes, logRecord.Attributes())
	msg := &graylog.Message{
		Version:      le.config.GELFMapping.Version,
		Host:         cleanAndStringifyAny(hostname),
		ShortMessage: cleanAndStringifyAny(shortmsg),
		FullMessage:  cleanAndStringifyAny(fullmsg),
		Timestamp:    timestamp.Unix(),
		Level:        level,
		Extra:        extra,
	}
	le.logger.Sugar().Debugf("Converted log record to Graylog message: %v", msg)
	return msg, nil
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
					le.logger.Sugar().Errorf("Error converting log record to Graylog message: %v", err)
					continue
				}
				le.logger.Sugar().Debugf("Enqueuing Graylog message: %v", msg)
				if err := le.graylogSender.SendToQueue(msg); err != nil {
					le.logger.Sugar().Warnf("Failed to enqueue Graylog message: %v", err)
				}
				le.logger.Sugar().Debugf("Graylog message added to queue successfully")
			}
		}
	}
	return nil
}

func cleanString(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if r == 0xFFFD || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func cleanAndStringifyAny(v interface{}) string {
	switch val := v.(type) {
	case string:
		return cleanString(val)
	case fmt.Stringer:
		return cleanString(val.String())
	case map[string]interface{}:
		if b, err := json.Marshal(val); err == nil {
			return cleanString(string(b))
		}
		return cleanString(fmt.Sprintf("%v", val))
	default:
		return cleanString(fmt.Sprintf("%v", val))
	}
}

func cleanAndStringifyOtelValue(v pcommon.Value) string {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return cleanString(v.Str())
	case pcommon.ValueTypeBool:
		return strconv.FormatBool(v.Bool())
	case pcommon.ValueTypeInt:
		return strconv.FormatInt(v.Int(), 10)
	case pcommon.ValueTypeDouble:
		return strconv.FormatFloat(v.Double(), 'f', -1, 64)
	case pcommon.ValueTypeMap:
		m := make(map[string]interface{})
		v.Map().Range(func(k string, sv pcommon.Value) bool {
			m[k] = sv.AsRaw()
			return true
		})
		if b, err := json.Marshal(m); err == nil {
			return cleanString(string(b))
		}
		return cleanString(fmt.Sprintf("%v", m))
	default:
		return cleanString(fmt.Sprintf("%v", v.AsRaw()))
	}
}

func (le *grayLogExporter) getTimestampAndLevel(logRecord plog.LogRecord) (time.Time, uint) {
	timestampval := logRecord.Timestamp()
	text := strings.ToLower(logRecord.SeverityText())
	severity := logRecord.SeverityNumber()
	var timestamp time.Time
	if timestampval == 0 {
		le.logger.Warn("Missing timestamp in log record, using current time as fallback")
		timestamp = time.Now()
	} else {
		timestamp = timestampval.AsTime()
	}

	switch text {
	case "emergency", "panic":
		return timestamp, 0
	case "alert":
		return timestamp, 1
	case "critical", "crit":
		return timestamp, 2
	case "error", "err":
		return timestamp, 3
	case "warning", "warn":
		return timestamp, 4
	case "notice":
		return timestamp, 5
	case "info":
		return timestamp, 6
	case "debug", "trace":
		return timestamp, 7
	}

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
	}

	return timestamp, 6
}
