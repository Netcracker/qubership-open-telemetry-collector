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
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

const (
	typeStr             = "graylogexporter"
	defaultBindEndpoint = "0.0.0.0:12201"
)

var (
	fieldmapping *GELFFieldMapping
	once         sync.Once
)

type TimeoutConfig struct {
	Timeout time.Duration
}

func loadGELFFieldMapping(cfg *Config) error {
	var err error
	once.Do(func() {
		fieldmapping, err = parseGELFFieldMapping(cfg)
	})
	return err
}

func GetGELFFieldMapping() *GELFFieldMapping {
	return fieldmapping
}

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		component.MustNewType(typeStr),
		createDefaultConfig,
		exporter.WithLogs(newgraylogExporter, component.StabilityLevelAlpha))
}

func createDefaultConfig() component.Config {
	return &Config{
		TCPAddrConfig: confignet.TCPAddrConfig{
			Endpoint: defaultBindEndpoint,
		},
		ConnPoolSize:                1,
		QueueSize:                   1000,
		MaxMessageSendRetryCnt:      1,
		MaxSuccessiveSendErrCnt:     5,
		SuccessiveSendErrFreezeTime: "1m",
		GELFMapping:                 *getDefaultGELFFields(),
	}
}

func parseGELFFieldMapping(cfg *Config) (*GELFFieldMapping, error) {
	var fieldMapping GELFFieldMapping
	raw := make(map[string]interface{})
	if err := mapstructure.Decode(cfg, &raw); err != nil {
		return nil, errors.New("failed to decode config to map")
	}

	if fmRaw, ok := raw["field_mapping"]; ok {
		if err := mapstructure.Decode(fmRaw, &fieldMapping); err != nil {
			return nil, errors.New("failed to decode field_mapping")
		}
	}
	return &fieldMapping, nil
}

func newgraylogExporter(
	ctx context.Context,
	set exporter.Settings,
	cfg component.Config,
) (exporter.Logs, error) {
	ltec, ok := cfg.(*Config)
	if !ok {
		return nil, errors.New("invalid configuration type")
	}

	if ltec.Endpoint == "" {
		return nil, errors.New("exporter config requires a non-empty 'endpoint'")
	}
	lte := createLogExporter(ltec, set)
	err := loadGELFFieldMapping(ltec)
	lte.config.GELFMapping = *GetGELFFieldMapping()

	if err != nil {
		return nil, errors.New("GELF field mapping is not parsable")
	}
	timeoutConfig := exporterhelper.TimeoutConfig{
		Timeout: time.Duration(0), // Set your desired timeout value here
	}
	return exporterhelper.NewLogs(
		ctx,
		set,
		cfg,
		lte.pushLogs,
		exporterhelper.WithStart(lte.start),
		exporterhelper.WithTimeout(timeoutConfig),
	)
}
