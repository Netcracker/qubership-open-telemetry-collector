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
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confignet"
)

type Config struct {
	confignet.TCPAddrConfig     `mapstructure:",squash"`
	ATLCfg                      ATLConfig `mapstructure:"arbitrary-traces-logging"`
	ConnPoolSize                int       `mapstructure:"connection-pool-size"`
	QueueSize                   int       `mapstructure:"queue-size"`
	MaxMessageSendRetryCnt      int       `mapstructure:"max-message-send-retry-count"`
	MaxSuccessiveSendErrCnt     int       `mapstructure:"max-successive-send-error-count"`
	SuccessiveSendErrFreezeTime string    `mapstructure:"successive-send-error-freeze-time"`
}

var _ component.Config = (*Config)(nil)

func (cfg *Config) Validate() error {
	if cfg.ConnPoolSize < 1 {
		return fmt.Errorf("connection-pool-size can not be less than 1 (actual value is %v)", cfg.ConnPoolSize)
	}
	if cfg.QueueSize < 1 {
		return fmt.Errorf("queue-size can not be less than 1 (actual value is %v)", cfg.QueueSize)
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
	return nil
}

type ATLConfig struct {
	SpanFilters  []ATLFilter `mapstructure:"span-filters"`
	TraceFilters []ATLFilter `mapstructure:"trace-filters"`
}

type ATLFilter struct { // ArbitraryTracesLoggingFilter
	ServiceNames []string            `mapstructure:"service-names"`
	Tags         map[string]string   `mapstructure:"tags"`
	Mapping      map[string][]string `mapstructure:"mapping"`
}
