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

package main

import (
	sentrymetricsconnector "otec/connector/sentrymetricsconnector"
	graylogexporter "otec/exporter/graylogexporter"
	logtcpexporter "otec/exporter/logtcpexporter"
	sentryreceiver "otec/receiver/sentryreceiver"

	spanmetricsconnector "github.com/open-telemetry/opentelemetry-collector-contrib/connector/spanmetricsconnector"
	prometheusexporter "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	healthcheckextension "github.com/open-telemetry/opentelemetry-collector-contrib/extension/healthcheckextension"
	pprofextension "github.com/open-telemetry/opentelemetry-collector-contrib/extension/pprofextension"
	filterprocessor "github.com/open-telemetry/opentelemetry-collector-contrib/processor/filterprocessor"
	probabilisticsamplerprocessor "github.com/open-telemetry/opentelemetry-collector-contrib/processor/probabilisticsamplerprocessor"
	transformprocessor "github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor"
	jaegerreceiver "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/jaegerreceiver"
	zipkinreceiver "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/zipkinreceiver"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/exporter"
	debugexporter "go.opentelemetry.io/collector/exporter/debugexporter"
	loggingexporter "go.opentelemetry.io/collector/exporter/loggingexporter"
	otlpexporter "go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	batchprocessor "go.opentelemetry.io/collector/processor/batchprocessor"
	"go.opentelemetry.io/collector/receiver"
	otlpreceiver "go.opentelemetry.io/collector/receiver/otlpreceiver"
)

func components() (otelcol.Factories, error) {
	var err error
	factories := otelcol.Factories{}

	factories.Extensions, err = extension.MakeFactoryMap(
		healthcheckextension.NewFactory(),
		pprofextension.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ExtensionModules = make(map[component.Type]string, len(factories.Extensions))
	factories.ExtensionModules[healthcheckextension.NewFactory().Type()] = "go.opentelemetry.io/collector/extension/github.com/open-telemetry/opentelemetry-collector-contrib/extension/healthcheckextension v0.106.1"
	factories.ExtensionModules[pprofextension.NewFactory().Type()] = "go.opentelemetry.io/collector/extension/pprofextension v0.106.1"

	factories.Receivers, err = receiver.MakeFactoryMap(
		otlpreceiver.NewFactory(),
		jaegerreceiver.NewFactory(),
		zipkinreceiver.NewFactory(),
		sentryreceiver.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ReceiverModules = make(map[component.Type]string, len(factories.Receivers))
	factories.ReceiverModules[otlpreceiver.NewFactory().Type()] = "go.opentelemetry.io/collector/receiver/otlpreceiver v0.106.1"
	factories.ReceiverModules[jaegerreceiver.NewFactory().Type()] = "go.opentelemetry.io/collector/receiver/jaegerreceiver v0.106.1"
	factories.ReceiverModules[zipkinreceiver.NewFactory().Type()] = "go.opentelemetry.io/collector/receiver/zipkinreceiver v0.106.1"
	factories.ReceiverModules[sentryreceiver.NewFactory().Type()] = "otec/receiver/sentryreceiver v1.1.17"

	factories.Exporters, err = exporter.MakeFactoryMap(
		debugexporter.NewFactory(),
		otlpexporter.NewFactory(),
		prometheusexporter.NewFactory(),
		loggingexporter.NewFactory(),
		logtcpexporter.NewFactory(),
		graylogexporter.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ExporterModules = make(map[component.Type]string, len(factories.Exporters))
	factories.ExporterModules[debugexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/debugexporter v0.106.1"
	factories.ExporterModules[otlpexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/otlpexporter v0.106.1"
	factories.ExporterModules[prometheusexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/prometheusexporter v0.106.1"
	factories.ExporterModules[loggingexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/loggingexporter v0.106.1"
	factories.ExporterModules[logtcpexporter.NewFactory().Type()] = "otec/exporter/logtcpexporter v1.1.17"
	factories.ExporterModules[graylogexporter.NewFactory().Type()] = "otec/exporter/graylogexporter v1.1.17"

	factories.Processors, err = processor.MakeFactoryMap(
		batchprocessor.NewFactory(),
		filterprocessor.NewFactory(),
		transformprocessor.NewFactory(),
		probabilisticsamplerprocessor.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ProcessorModules = make(map[component.Type]string, len(factories.Processors))
	factories.ProcessorModules[batchprocessor.NewFactory().Type()] = "go.opentelemetry.io/collector/processor/batchprocessor v0.106.1"
	factories.ProcessorModules[filterprocessor.NewFactory().Type()] = "go.opentelemetry.io/collector/processor/filterprocessor v0.106.1"
	factories.ProcessorModules[transformprocessor.NewFactory().Type()] = "go.opentelemetry.io/collector/processor/transformprocessor v0.106.1"
	factories.ProcessorModules[probabilisticsamplerprocessor.NewFactory().Type()] = "go.opentelemetry.io/collector/processor/probabilisticsamplerprocessor v0.106.1"

	factories.Connectors, err = connector.MakeFactoryMap(
		spanmetricsconnector.NewFactory(),
		sentrymetricsconnector.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ConnectorModules = make(map[component.Type]string, len(factories.Connectors))
	factories.ConnectorModules[spanmetricsconnector.NewFactory().Type()] = "go.opentelemetry.io/collector/connector/spanmetricsconnector v0.106.1"
	factories.ConnectorModules[sentrymetricsconnector.NewFactory().Type()] = "otec/connector/sentrymetricsconnector v1.1.17"

	return factories, nil
}
