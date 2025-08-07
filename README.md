# Open-telemetry-collector

## Introduction

The implementation is based on
[open-telemetry/opentelemetry-collector](https://github.com/open-telemetry/opentelemetry-collector)
and
[open-telemetry/opentelemetry-collector-contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib)
versions of open-telemetry-collector.

For more details about open-telemetry approach for processing traces, metrics and logs see
[https://opentelemetry.io/docs](https://opentelemetry.io/docs/)

Open-telemetry-collector approach for configuration see: <https://opentelemetry.io/docs/collector/configuration/>.
New modules development approach see: <https://opentelemetry.io/docs/collector/building/>.

## Supported modules

The following third-party modules are supported by this implementation of open-telemetry-collector.

Receivers:

* [jaegerreceiver](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/receiver/jaegerreceiver/README.md)
* [otlpreceiver](https://github.com/open-telemetry/opentelemetry-collector/blob/main/receiver/otlpreceiver/README.md)
* [zipkinreceiver](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/receiver/zipkinreceiver)

Processors:

* [batchprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/extension/pprofextension/README.md)
* [filterprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/filterprocessor/README.md)
* [probabilisticsamplerprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/probabilisticsamplerprocessor/README.md)
* [tailsamplingprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/tailsamplingprocessor/README.md)
* [transformprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/transformprocessor/README.md)

Connectors:

* [spanmetricsconnector](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/connector/spanmetricsconnector/README.md)

Exporters:

* [debugexporter](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/debugexporter/README.md)
* [jaegerexporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.85.0/exporter/jaegerexporter)
* [otlpexporter](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/otlpexporter/README.md)
* [otlphttpexporter](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/otlphttpexporter/README.md)
* [prometheusexporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/exporter/prometheusexporter/README.md)

Extension:

* [healthcheckextension](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/extension/healthcheckextension/README.md)
* [pprofextension](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/extension/pprofextension/README.md)

All third-party and custom modules are listed in [builder-config.yaml](builder-config.yaml).

## Custom module

Also there are custom implementations receive Sentry traces (as Sentry envelop), process them and send
to Tracing and Logging exporters.

Receivers:

* [sentryreceiver](receiver/sentryreceiver/README.md), see also the [document](docs/sentry-receiver.md#sentry-envelope-mapping-to-jaeger-traces)

Connectors:

* [logtcpexporter](exporter/logtcpexporter/README.md), see also the [document](docs/sentry-receiver.md#sentry-envelope-to-logs-records-graylog-mapping)

Exporters:

* [sentrymetricsconnector](connector/sentrymetricsconnector/README.md), see also the [document](docs/sentry-receiver.md#sentry-envelope-to-metrics)
