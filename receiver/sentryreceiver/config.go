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

package sentryreceiver

import (
	"go.opentelemetry.io/collector/config/confighttp"
)

// Config represents the receiver config settings within the collector's config.yaml
type Config struct {
	confighttp.ServerConfig        `mapstructure:",squash"` // squash ensures fields are correctly decoded in embedded struct
	HttpQueryParamValuesToAttrs    []string                 `mapstructure:"http-query-param-values-to-attrs"`
	HttpQueryParamExistenceToAttrs []string                 `mapstructure:"http-query-param-existence-to-attrs"`
	LevelEvaluationStrategy        string                   `mapstructure:"level-evaluation-strategy"`
	ContextSpanAttributesList      []string                 `mapstructure:"context-span-attributes-list"`
}

func (cfg *Config) Validate() error {
	return nil
}
