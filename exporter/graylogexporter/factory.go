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
	"errors"
	"otec/exporter/logtcpexporter"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

const (
	typeStr             = "graylogexporter"
	defaultBindEndpoint = "0.0.0.0:12201"
)

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		component.MustNewType(typeStr),
		createDefaultConfig,
		exporter.WithLogs(newgraylogLogExporter, component.StabilityLevelAlpha))
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

func newgraylogLogExporter(
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
