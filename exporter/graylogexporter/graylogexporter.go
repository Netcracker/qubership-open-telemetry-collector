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
		le.config.QueueSize,
		le.config.MaxMessageSendRetryCnt,
		le.config.MaxSuccessiveSendErrCnt,
		freezeTime,
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
		return attributes, "no message provided", nil
	case pcommon.ValueTypeEmpty:
		return nil, "no message provided", fmt.Errorf("log body is empty")
	default:
		return nil, "no message provided", fmt.Errorf("unsupported body type: %v", body.Type())
	}
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
	case pcommon.ValueTypeMap:
		m := make(map[string]interface{})
		val.Map().Range(func(k string, v pcommon.Value) bool {
			if s, ok := getStringFromPcommonValue(v); ok {
				m[k] = s
			} else {
				m[k] = fmt.Sprintf("%v", v)
			}
			return true
		})
		b, err := json.Marshal(m)
		if err != nil {
			return "", false
		}
		return string(b), true
	case pcommon.ValueTypeSlice:
		arr := make([]interface{}, 0, val.Slice().Len())
		for i := 0; i < val.Slice().Len(); i++ {
			v := val.Slice().At(i)
			if s, ok := getStringFromPcommonValue(v); ok {
				arr = append(arr, s)
			} else {
				arr = append(arr, fmt.Sprintf("%v", v))
			}
		}
		b, err := json.Marshal(arr)
		if err != nil {
			return "", false
		}
		return string(b), true
	case pcommon.ValueTypeBytes:
		return string(val.Bytes().AsRaw()), true
	default:
		return "", false
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
	if val, ok := logAttrs.Get(key); ok {
		if s, ok := getStringFromPcommonValue(val); ok {
			le.logger.Debug("Using log attribute", zap.String("key", key), zap.String("value", s))
			return s
		}
	}
	le.logger.Debug("value not found in attributes", zap.String("key", key))
	return fmt.Sprintf("%v not found", key)
}

func extractPcommonAttributes(attrs pcommon.Map) map[string]string {
	extra := make(map[string]string)

	attrs.Range(func(k string, v pcommon.Value) bool {
		switch v.Type() {
		case pcommon.ValueTypeStr:
			extra[k] = v.AsString()
		case pcommon.ValueTypeBool:
			extra[k] = strconv.FormatBool(v.Bool())
		case pcommon.ValueTypeInt:
			extra[k] = strconv.FormatInt(v.Int(), 10)
		case pcommon.ValueTypeDouble:
			extra[k] = fmt.Sprintf("%f", v.Double())
		case pcommon.ValueTypeMap:
			nested := make(map[string]interface{})
			v.Map().Range(func(subKey string, subVal pcommon.Value) bool {
				if s, ok := getStringFromPcommonValue(subVal); ok && s != "" {
					nested[subKey] = s
				}
				return true
			})
			if len(nested) > 0 {
				if jsonStr, err := json.Marshal(nested); err == nil {
					extra[k] = string(jsonStr)
				}
			}
		case pcommon.ValueTypeSlice:
			slice := make([]interface{}, v.Slice().Len())
			for i := 0; i < v.Slice().Len(); i++ {
				if s, ok := getStringFromPcommonValue(v.Slice().At(i)); ok {
					slice[i] = s
				}
			}
			if len(slice) > 0 {
				if jsonStr, err := json.Marshal(slice); err == nil {
					extra[k] = string(jsonStr)
				}
			}
		case pcommon.ValueTypeBytes:
			extra[k] = string(v.Bytes().AsRaw())
		default:
			extra[k] = fmt.Sprintf("%v", v)
		}
		return true
	})

	return extra
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
		extra[k] = fmt.Sprintf("%v", v)
	}

	for k, v := range extractPcommonAttributes(logRecord.Attributes()) {
		extra[k] = v
	}
	for k, v := range extractPcommonAttributes(resourceAttrs) {
		extra["resource."+k] = v
	}

	fullmsg := le.getMappedValue(le.config.GELFMapping.FullMessage, attributes, logRecord.Attributes())
	if message != "" && (fullmsg == "" || strings.Contains(strings.ToLower(fullmsg), "not found")) {
		fullmsg = message
	} else {
		fullmsg = "No message provided"
	}
	shortmsg := le.getMappedValue(le.config.GELFMapping.ShortMessage, attributes, logRecord.Attributes())
	if message != "" && (shortmsg == "" || strings.Contains(strings.ToLower(fullmsg), "not found")) {
		shortmsg = message
	} else {
		shortmsg = "No short message provided"
	}
	hostname := le.getMappedValue(le.config.GELFMapping.Host, attributes, logRecord.Attributes())
	msg := &graylog.Message{
		Version:      le.config.GELFMapping.Version,
		Host:         hostname,
		ShortMessage: shortmsg,
		FullMessage:  fullmsg,
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
				le.logger.Sugar().Debugf("Enqueuing Graylog message: version=%s host=%s shortmsg=%s fullmsg=%s timestamp=%d level=%d extra=%v",
					msg.Version, msg.Host, msg.ShortMessage, msg.FullMessage, msg.Timestamp, msg.Level, msg.Extra)
				if err := le.graylogSender.SendToQueue(msg); err != nil {
					le.logger.Sugar().Warnf("Failed to enqueue Graylog message: %v", err)
				}
				le.logger.Sugar().Debugf("Graylog message added to queue successfully")
			}
		}
	}
	return nil
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
