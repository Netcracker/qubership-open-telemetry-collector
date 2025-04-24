// Copyright 2024 Qubership

package logexporter

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"otec/exporter/logtcpexporter"
	"otec/exporter/logtcpexporter/graylog"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
)

type logExporter struct {
	url           string
	graylogSender *graylog.GraylogSender
	settings      exporter.Settings
	logger        *zap.Logger
	config        *logtcpexporter.Config
}

func createLogExporter(cfg *logtcpexporter.Config, settings exporter.Settings) *logExporter {
	return &logExporter{
		url:      strings.Trim(cfg.Endpoint, " /"),
		settings: settings,
		logger:   settings.Logger,
		config:   cfg,
	}
}

func (le *logExporter) start(_ context.Context, host component.Host) (err error) {
	var address string
	var port uint64
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
	)

	return nil
}

func (le *logExporter) processLogRecords(scopeLogs plog.ScopeLogs) {
	for i := 0; i < scopeLogs.LogRecords().Len(); i++ {
		logRecord := scopeLogs.LogRecords().At(i)
		if err := le.sendLogToGraylog(logRecord); err != nil {
			log.Printf("Error sending log: %v", err)
		}
	}
}

// pushLogs sends logs to Graylog (using ResourceLogs and ScopeLogs)
func (le *logExporter) pushLogs(ctx context.Context, logs plog.Logs) error {
	var wg sync.WaitGroup
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		resourceLog := logs.ResourceLogs().At(i)
		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)
			wg.Add(1)
			go func(scopeLog plog.ScopeLogs) {
				defer wg.Done()
				le.processLogRecords(scopeLog)
			}(scopeLog)
		}
	}
	wg.Wait()

	return nil
}

func (le *logExporter) sendLogToGraylog(logRecord plog.LogRecord) error {

	messageStr := logRecord.Body().AsString()
	timestamp, level := le.getTimestampAndLevel(logRecord)

	msg := graylog.Message{
		Host:         "open-telemetry-collector",
		ShortMessage: messageStr,
		FullMessage:  "",
		Timestamp:    timestamp.Unix(),
		Level:        level,
		Extra:        map[string]string{},
	}

	err := le.graylogSender.SendToQueue(&msg)
	if err != nil {
		le.logger.Sugar().Errorf("Message with timestamp %v has not been put to the graylog queue: %+v\n", msg.Timestamp, err)
		return err
	}

	le.logger.Sugar().Debugf("Message with timestamp %v has been put successfully to the graylog queue\n", msg.Timestamp)
	return nil
}

func (le *logExporter) getTimestampAndLevel(logRecord plog.LogRecord) (time.Time, uint) {
	timestamp := logRecord.Timestamp().AsTime()
	var level uint = 6
	return timestamp, level
}
