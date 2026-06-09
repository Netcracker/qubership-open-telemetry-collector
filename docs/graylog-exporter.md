# Graylog Exporter Configuration Specifications

Following are the steps to send logs from a Kubernetes cluster using:
**Fluent Bit** → **OpenTelemetry Collector (OTEC)** → **Graylog**.

## 1. Configure Fluent Bit to forward logs to OTEC

### a. Include the output section in the config

Example:

```ini
@INCLUDE /fluent-bit/etc/output-graylog.conf
```

### b. Configure the following section in the Fluent Bit config

`output-graylog.conf`:

```ini
[OUTPUT]
    Name                           opentelemetry
    Match                          *
    Host                           <opentelemetry service where the logs can be forwarded>
    Port                           <port on which OTEC is receiving the logs (receivers port)>
    net.connect_timeout            20s  # connection timeout value
    net.max_worker_connections     35   # maximum connections
    net.dns.mode                   TCP  # mode of connection (currently only TCP is supported)
    net.dns.resolver               LEGACY # DNS resolver
    tls                            Off  # TLS settings
    tls.verify                     Off  # TLS settings
    retry_limit                    False
    logs_uri                       /v1/logs  # the default path where OTEC is receiving logs
    Log_response_payload           True
    logs_severity_text_message_key level # parsed from Fluent Bit config
    logs_body_key_attributes       True  # keep this true for forwarding additional attributes
```

---

## 2. Configure/Customize OTEC parameters

### a. Enable logging

```yaml
OTEC_ENABLE_LOGGING: true  # enables logging to use Graylog exporter
```

### b. Configure Graylog host

```yaml
GRAYLOG_COLLECTOR_HOST: <graylog host IP>
```

### c. Configure Graylog port

```yaml
<graylog host port to receive logs>
```

### d. Configure Graylog exporter parameters (customizable)

```yaml
OTEC_GRAYLOGEXPORTER_PARAMETERS:
    connection-pool-size: 1
    queue-size: 1000
    max-message-send-retry-count: 1
    max-successive-send-error-count: 5
    successive-send-error-freeze-time: 1m
```

### e. Configure mandatory GELF message fields

```yaml
OTEC_GELF_FIELD_MAPPING:
    version: "1.1"
    host: "hostname"
    short-message: "log"
    full-message: "log"
    level: "level"
```

### f. (Optional) Add log deduplication processor parameters

```yaml
OTEC_LOGDEDUP_PROCESSOR_PARAMETERS:
    include_fields:
        - attributes.hostname
    interval: 60s
    log_count_attribute: logdup_count
```

---

> **Note:** For more filters for log deduplication processor, see
> [logdedup documentation](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/logdedupprocessor#readme).
