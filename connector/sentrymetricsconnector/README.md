# Sentry Metrics connector

The Sentry Metrics Connector for the QuberShip Open Telemetry Collector enables integration of Sentry Envelops into
observability pipeline. This connector collects, processes, and forwards metrics from supported sources to Sentry,
allowing you to monitor application performance and errors in real time.

## Purpose

Generate metrics based on Sentry Envelops with QuberShip Open Telemetry Collector.

## Configuration Options

- `dsn`: Sentry Data Source Name (DSN) for authentication.
- `metrics`: List of metric names or patterns to forward.
- `mapping`: Optional rules for renaming or transforming metrics.
- `batch_size`: Number of metrics to send per batch.
- `flush_interval`: Time interval for flushing metrics to Sentry.

## Example Configuration

```yaml
connector:
    sentrymetrics:
        dsn: "https://<public_key>@sentry.io/<project_id>"
        metrics:
            - "http_requests_total"
            - "db_query_duration_seconds"
        mapping:
            http_requests_total: "requests.count"
            db_query_duration_seconds: "db.query.time"
        batch_size: 100
        flush_interval: 10s
```
