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
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"otec/exporter/graylog"
	"otec/exporter/logtcpexporter"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

type graylogexporter struct {
	url           string
	graylogSender *graylog.GraylogSender
	settings      exporter.Settings
	logger        *zap.Logger
	config        *logtcpexporter.Config
}

func createLogExporter(cfg *logtcpexporter.Config, settings exporter.Settings) *graylogexporter {
	return &graylogexporter{
		url:      strings.Trim(cfg.Endpoint, " /"),
		settings: settings,
		logger:   settings.Logger,
		config:   cfg,
	}
}

func (le *graylogexporter) start(_ context.Context, host component.Host) (err error) {
	var address string
	var port uint64
	useBulk := true
	endpointSplitted := strings.Split(le.url, ":")
	if len(endpointSplitted) == 1 {
		address = endpointSplitted[0]
		port = 12201
	} else if len(endpointSplitted) > 1 {
		address = endpointSplitted[0]
		port, err = strconv.ParseUint(endpointSplitted[1], 10, 64)
		if err != nil {
			errMsg := fmt.Sprintf("Error parsing %v port number to uint64 : %+v\n", endpointSplitted[1], err)
			le.logger.Error(errMsg)
			return fmt.Errorf(errMsg)
		}
	}

	freezeTime, err := time.ParseDuration(le.config.SuccessiveSendErrFreezeTime)
	if err != nil {
		errMsg := fmt.Sprintf("le.config.successiveSendErrFreezeTime is not parseable : %+v", err)
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
		useBulk,
	)

	return nil
}

func (le *graylogexporter) processLogRecords(scopeLogs plog.ScopeLogs) []string {
	var messages []string
	for i := 0; i < scopeLogs.LogRecords().Len(); i++ {
		logRecord := scopeLogs.LogRecords().At(i)
		msgStr, err := le.formatLogRecordToGELF(logRecord)
		if err == nil {
			messages = append(messages, msgStr)
		} else {
			le.logger.Sugar().Errorf("Error formatting log: %v", err)
		}
	}
	return messages
}

func (le *graylogexporter) formatLogRecordToGELF(logRecord plog.LogRecord) (string, error) {
	timestamp, level := le.getTimestampAndLevel(logRecord)

	gelf := map[string]interface{}{
		"version":       "1.1",
		"host":          "open-telemetry-collector",
		"short_message": logRecord.Body().AsString(),
		"timestamp":     float64(timestamp.UnixNano()) / 1e9,
		"level":         level,
	}

	logRecord.Attributes().Range(func(k string, v pcommon.Value) bool {
		gelf["_"+k] = v.AsString()
		return true
	})

	msgBytes, err := json.Marshal(gelf)
	if err != nil {
		return "", err
	}
	return string(msgBytes), nil
}

func (le *graylogexporter) pushLogs(ctx context.Context, logs plog.Logs) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allMessages []string

	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLog := logs.ResourceLogs().At(i)
		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)

			wg.Add(1)
			go func(scopeLog plog.ScopeLogs) {
				defer wg.Done()
				messages := le.processLogRecords(scopeLog)
				mu.Lock()
				allMessages = append(allMessages, messages...)
				mu.Unlock()
			}(scopeLog)
		}
	}

	wg.Wait()
	return le.sendBulkToGraylog(allMessages)
}

func (le *graylogexporter) sendBulkToGraylog(messages []string) error {
	var buffer strings.Builder
	for _, msg := range messages {
		buffer.WriteString(msg)
		buffer.WriteByte(0)
	}
	return le.graylogSender.SendRaw(buffer.String())
}

func (le *graylogexporter) getTimestampAndLevel(logRecord plog.LogRecord) (time.Time, uint) {
	timestamp := logRecord.Timestamp().AsTime()

	text := strings.ToLower(logRecord.SeverityText())
	switch text {
	case "fatal":
		return timestamp, 2
	case "error":
		return timestamp, 3
	case "warn", "warning":
		return timestamp, 4
	case "info":
		return timestamp, 6
	case "debug", "trace":
		return timestamp, 7
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
	case severity >= plog.SeverityNumberTrace && severity <= plog.SeverityNumberTrace4:
		return timestamp, 7
	}

	return timestamp, 6 // INFO
}
