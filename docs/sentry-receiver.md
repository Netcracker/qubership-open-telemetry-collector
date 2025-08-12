# Sentry receiver specification

## Envelope

**Envelope** - http/grpc/etc request with body in
[ndjson](https://github.com/ndjson/ndjson.github.io/blob/master/libraries.html)
format.

Usually it has structure like:

```json
{ ... } // header - contains information who and how send that event.
{ "type": "event|transaction|session..." } // message type (json with one field)
{ ... } // content of envelope.
```

### Envelope Types

Sentry uses 3 types of messages (envelopes) to deliver metrics, traces, exceptions and etc.:

- **`session`** - envelope of this type means that user started a `session` on the page.
  This event just indicates that a User-Agent opened the page.
- **`transaction`** - this envelope contains `spans`, `breadcrumbs`, `measurements` and many other information.
  Usually transaction has start time and end time.
- **`event`** - this envelope contains information about errors, exceptions or manually triggered `events`
  in the single point of time.

## Response types

Sentry SDK produces _Envelopes_ to the http endpoint. `open-telemetry-collector` should respond with correct response

- `type: "session"`

```json
{}
```

- `type: "event|transaction"`

```json
{ "id": "d73ca72181e440ee94ff7782ceca65c5" } // event_id from envelope header
```

## Sentry Envelope mapping to Jaeger traces

In the table below you can find mapping for fields of Sentry envelopes of types **event** and **transaction**
to the opentelemetry trace attributes.

<!-- markdownlint-disable line-length -->
| Event or Transaction field/HTTP     | Otel Trace                                             | Description                   | Envelope Event | Comment                                                              |
| ----------------------------------- | ------------------------------------------------------ | ----------------------------- | -------------- | -------------------------------------------------------------------- |
| `{transaction} {context.trace.op}`  | `rootSpan.name`                                        | The trace name                | `transaction`  |                                                                      |
| `request.headers['x-service-name']` | `"service.name"`                                       | Service name                  | any            | supposed that `x-service-id` will contain a name of the microservice |
| `environment`                       | `environment`                                          | The name of the telemetry SDK | any            |                                                                      |
| `measurements.*`                    | `measurements.{measurements.value} {measurements.unit}` | -                             | any            |                                                                      |
| `start_timestamp`                   | `rootSpan.start_time_unix_nano`                        | -                             | `transaction`  |                                                                      |
| `timestamp`                         | `end_time_unix_nano`                                   | -                             | any            |                                                                      |
| `context.trace.trace_id`            | `trace_id`                                             | -                             | any            |                                                                      |
| `context.trace.span_id`             | `span_id`                                              | -                             | any            |                                                                      |
| `transaction`                       | `transaction`                                          | -                             | any            |                                                                      |
| `dist`                              | `dist`                                                 | -                             | any            |                                                                      |
<!-- markdownlint-enable line-length -->

In the table below you can find mapping of Sentry **spans** fields to the attributes of opentelemetry spans:

<!-- markdownlint-disable line-length -->
| Sentry `span`          | Otel Span                   | Description | Comment |
| ---------------------- | --------------------------- | ----------- | ------- |
| `span.start_timestamp` | `span.start_time_unix_nano` | -           |         |
| `span.timestamp`       | `span.end_time_unix_nano`   | -           |         |
| `span.trace_id`        | `span.trace_id`             |             |         |
| `span.span_id`         | `span.span_id`              |             |         |
| `span.parent_span_id`  | `span.parent_span_id`       |             |         |
| `span.op`              | `span.name`                 | -           |         |
| `span.status`          | `span.status.code`          | -           |         |
| `span.data[*]`         | `span.[*]`                  | -           |         |
| `span.origin`          | `span.origin`               | -           |         |
| `span.description`     | `span.description`          | -           |         |
<!-- markdownlint-enable line-length -->

## Sentry Envelope to Logs records (Graylog mapping)

LogTCP Exporter allows to log certain data from sentry envelopes to the Graylog. For now only sentry envelopes of event type can be logged:  

### `type: "event"`

<!-- markdownlint-disable line-length -->
| Event field                                                                      | Graylog field                  | Description                                                                             | Comment                                                                                                              |
| -------------------------------------------------------------------------------- | ------------------------------ | --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `contexts.trace.span_id`                                                         | `span_id`                      |                                                                                         |                                                                                                                      |
| `contexts.trace.trace_id`                                                        | `trace_id`                     |                                                                                         |                                                                                                                      |
| `'frontend'`                                                                     | `component`                    |                                                                                         |                                                                                                                      |
| `level` (need to map to the level_id)                                            | `level`                        |                                                                                         |                                                                                                                      |
| `'open-telemetry-collector'`                                                     | `facility`                     |                                                                                         |                                                                                                                      |
| `{sdk.name}@{sdk.version}`                                                       | `sdk`?                         |                                                                                         |                                                                                                                      |
| `message` or `context.Error.*` or `exception.values` or constant `empty_message` | `message`                      |                                                                                         |                                                                                                                      |
| `exception.values`                                                               | `stacktrace` or `full_message` | `exception.values` - list of chained exceptions, they are should be joined to one line. | [https://develop.sentry.dev/sdk/event-payloads/exception/](https://develop.sentry.dev/sdk/event-payloads/exception/) |
| `timestamp`                                                                      | `time`,`timestamp`             |                                                                                         |                                                                                                                      |
| `event_id`                                                                       | `event_id`                     |                                                                                         |                                                                                                                      |
| `release` or constant `empty_version`                                            | `version`                      |                                                                                         |                                                                                                                      |
| `req.headers.x-service-id`                                                       | `name`                         |                                                                                         |                                                                                                                      |
| `platform`                                                                       | `platform`                     |                                                                                         |                                                                                                                      |
| `user.id`                                                                        | `user_id`                      |                                                                                         | _new field_                                                                                                          |
| `tags.transaction`                                                               | `transaction`                  |                                                                                         | _new field_                                                                                                          |
| `logger`                                                                         | `category`                     | if logger present, use it in category field. Otherwise use `frontend-event` value       | _new field_                                                                                                          |
| `request.url`                                                                    | `url`                          |                                                                                         | _new field_                                                                                                          |
| `request.headers.User-Agent`                                                     | `browser`                      |                                                                                         | _new field_                                                                                                          |
<!-- markdownlint-enable line-length -->

In case **`event.level == "error"`** the `breadcrumbs` of `event` are also logged as separate log records.

All fields from `event` should be the same, but they can be overridden by the `breadcrumb` field.

#### `breadcrumb type: "http"`

<!-- markdownlint-disable line-length -->
| Event field                                      | Graylog field      | Description     | Comment |
| ------------------------------------------------ | ------------------ | --------------- | ------- |
| `breadcrumb?.level`                              | `level`            | only if present |         |
| `breadcrumb.timestamp`                           | `time`,`timestamp` |                 |         |
| `breadcrumb.category`                            | `category`         |                 |         |
| `{breadcrumb.data.method} {breadcrumb.data.url}` | `message`          |                 |         |
| `breadcrumb.data.status_code`                    | `status`           |                 |         |
<!-- markdownlint-enable line-length -->

#### `breadcrumb category: "navigation"`

<!-- markdownlint-disable line-length -->
| Event field                                                                | Graylog field      | Description | Comment |
| -------------------------------------------------------------------------- | ------------------ | ----------- | ------- |
| `breadcrumb.timestamp`                                                     | `time`,`timestamp` |             |         |
| `breadcrumb.category`                                                      | `'navigation'`     |             |         |
| `Browser navigation from: {breadcrumb.data.from} to: {breadcrumb.data.to}` | `message`          |             |         |
<!-- markdownlint-enable line-length -->

#### `breadcrumb category: "console"`

<!-- markdownlint-disable line-length -->
| Event field            | Graylog field      | Description | Comment                   |
| ---------------------- | ------------------ | ----------- | ------------------------- |
| `breadcrumb?.level`    | `level`            |             | Maybe skip `info` levels? |
| `breadcrumb.timestamp` | `time`,`timestamp` |             |                           |
| `breadcrumb.category`  | `'console'`        |             |                           |
| `breadcrumb.message`   | `message`          |             |                           |
<!-- markdownlint-enable line-length -->

### `type: "session"`

Envelopes with type `session` are not logged to the logging system.

## Sentry Envelope to Metrics

SentryMetrics Connector allows to generate metrics for each type of sentry envelopes below:  

### `type: "session"` (Metrics)

- sentry_session_exited_count - allows to monitor amount of unique sessions with `status: "exited"` when session ends.

### `type: "transaction"` (Metrics)

- sentry_measurements_statistic - allows to monitor Browser Web Vitals - measurements and duration of transactions - for each `{transaction} {context.trace.op}`.

### `type: "event"` (Metrics)

- sentry_event_count - allows to monitor amount of sentry events by `event.level`
