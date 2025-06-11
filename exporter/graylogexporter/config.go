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
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confignet"
)

type GELFFieldMapping struct {
	Version      string `mapstructure:"version"`
	Host         string `mapstructure:"host"`
	ShortMessage string `mapstructure:"short-message"`
	FullMessage  string `mapstructure:"full-message"`
	Level        string `mapstructure:"level"`
}

type Config struct {
	confignet.TCPAddrConfig     `mapstructure:",squash"`
	GELFMapping                 GELFFieldMapping `mapstructure:"field-mapping"`
	ConnPoolSize                int              `mapstructure:"connection-pool-size"`
	BatchSize                   int              `mapstructure:"batch-size"`
	MaxMessageSendRetryCnt      int              `mapstructure:"max-message-send-retry-count"`
	MaxSuccessiveSendErrCnt     int              `mapstructure:"max-successive-send-error-count"`
	SuccessiveSendErrFreezeTime string           `mapstructure:"successive-send-error-freeze-time"`
	BatchWorkerFlushInterval    string           `mapstructure:"batch-worker-flush-interval"`
}

func getDefaultGELFFields() *GELFFieldMapping {
	return &GELFFieldMapping{
		Version:      "1.1",
		Host:         "open-telemetry-collector",
		ShortMessage: "short-message",
		FullMessage:  "full-message",
		Level:        "info",
	}
}

var _ component.Config = (*Config)(nil)

func (cfg *Config) Validate() error {
	if cfg.ConnPoolSize < 1 {
		return fmt.Errorf("connection-pool-size can not be less than 1 (actual value is %v)", cfg.ConnPoolSize)
	}
	if cfg.BatchSize < 1 {
		return fmt.Errorf("batch-size can not be less than 1 (actual value is %v)", cfg.BatchSize)
	}
	if cfg.MaxMessageSendRetryCnt < 0 {
		return fmt.Errorf("max-message-send-retry-count can not be negative (actual value is %v)", cfg.MaxMessageSendRetryCnt)
	}
	if cfg.MaxSuccessiveSendErrCnt < 0 {
		return fmt.Errorf("max-successive-send-error-count can not be negative (actual value is %v)", cfg.MaxSuccessiveSendErrCnt)
	}
	_, err := time.ParseDuration(cfg.SuccessiveSendErrFreezeTime)
	if err != nil {
		return fmt.Errorf("successive-send-error-freeze-time is not parseable : %+v", err)
	}
	_, err = time.ParseDuration(cfg.BatchWorkerFlushInterval)
	if err != nil {
		return fmt.Errorf("batch-worker-flush-interval is not parseable : %+v", err)
	}
	return nil
}
