// Copyright 2024 Qubership

package logexporter

import (
	"context"
	"errors"
	"otec/exporter/logtcpexporter"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

const (
	typeStr             = "logexporter"
	defaultBindEndpoint = "0.0.0.0:12201"
)

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		component.MustNewType(typeStr),
		createDefaultConfig,
		exporter.WithLogs(newLogExporter, component.StabilityLevelAlpha))
}

func createDefaultConfig() component.Config {
	return &logtcpexporter.Config{
		TCPAddrConfig: confignet.TCPAddrConfig{
			Endpoint: defaultBindEndpoint,
		},
		ConnPoolSize:                1,
		QueueSize:                   1024,
		MaxMessageSendRetryCnt:      1,
		MaxSuccessiveSendErrCnt:     5,
		SuccessiveSendErrFreezeTime: "1m",
	}
}

func newLogExporter(
	ctx context.Context,
	set exporter.Settings,
	cfg component.Config,
) (exporter.Logs, error) {
	ltec := cfg.(*logtcpexporter.Config)

	if ltec.Endpoint == "" {
		return nil, errors.New("exporter config requires a non-empty 'endpoint'")
	}

	lte := createLogExporter(ltec, set)
	return exporterhelper.NewLogsExporter(
		ctx,
		set,
		cfg,
		lte.pushLogs,
		exporterhelper.WithStart(lte.start),
		exporterhelper.WithTimeout(exporterhelper.TimeoutSettings{Timeout: 0}),
	)
}
