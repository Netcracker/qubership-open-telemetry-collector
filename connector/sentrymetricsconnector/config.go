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

package sentrymetricsconnector

type Config struct {
	SentryMeasurementsCfg SentryMeasurementsConfig `mapstructure:"sentry_measurements"`
	SentryEventCountCfg   SentryEventCountConfig   `mapstructure:"sentry_events"`
}

type SentryMeasurementsConfig struct {
	DefaultBuckets []float64                                  `mapstructure:"default_buckets"`
	DefaultLabels  map[string]string                          `mapstructure:"default_labels"`
	Custom         map[string]*CustomSentryMeasurementsConfig `mapstructure:"custom"`
}

type CustomSentryMeasurementsConfig struct {
	Buckets []float64          `mapstructure:"buckets"`
	Labels  *map[string]string `mapstructure:"labels"`
}

type SentryEventCountConfig struct {
	Labels map[string]string `mapstructure:"labels"`
}

func (c *Config) Validate() error {
	return nil
}
