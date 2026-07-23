# Qubership OpenTelemetry Collector — troubleshooting

## Rollout and restart risk

Changes to the collector configuration normally start a Deployment rollout. The chart uses `RollingUpdate` with one
replica and `maxSurge: 25%`, so Kubernetes normally starts a replacement pod before terminating the old one. This
reduces downtime risk but does not eliminate it.

**DANGEROUS — a rollout or restart can interrupt ingestion and lose telemetry held in memory.** The risk applies when
the cluster cannot schedule the surge pod, the rollout strategy is `Recreate` or effectively permits the only ready
pod to stop, the old pod has already failed, a manual restart removes it, or the replacement never becomes ready.
Terminating a pod also discards its in-memory tail-sampling state and exporter queues. Before applying a change:

1. Check the effective Deployment strategy and available cluster capacity.
2. Keep the old pod running until the replacement is `Ready` whenever the situation allows it.
3. Monitor the rollout and be prepared to roll back if the replacement does not become ready.

Sections below link back to this warning when an action starts or forces a rollout.

## Collector startup and configuration

Every case in this section ends the same way from Kubernetes' point of view: the container exits with code 1 and the
pod enters `CrashLoopBackOff`. The distinguishing evidence is near the end of the *previous* container's log, so start
there and keep the complete error block.

The collector prints startup failures through cobra, which prefixes them with the literal `Error:` and a space.
`collector/main.go:51-55` deliberately does not log the error a second time. Cobra's error can span several lines
when configuration decoding reports multiple failures:

```bash
kubectl -n <namespace> logs <pod-name> --previous --tail=20
```

### Collector exits immediately and the rendered otel.yaml is empty

**Symptoms:**

* The pod enters `CrashLoopBackOff` right after a `helm upgrade` that changed `OTEC_INSTALLATION_MODE`.
* The previous container's log holds a single `Error:` line mentioning an empty or incomplete configuration, matching
  one of `empty configuration file`, `no receiver configuration specified in config`, or
  `no exporter configuration specified in config`.
* The `opentelemetry-collector-config` ConfigMap exists and `helm install` reported success.
* The `otel.yaml` key in that ConfigMap is present but has no content under it.

**Root cause:**

The config template has exactly two mode branches and no fallback. In
`charts/open-telemetry-collector/templates/metrics_collector_config.yaml` the branches are
`{{- if eq .Values.OTEC_INSTALLATION_MODE "SPAN_METRICS_PROCESSOR" }}` (line 12) and
`{{- else if eq .Values.OTEC_INSTALLATION_MODE "SENTRY_ENVELOPES_PROCESSING" }}` (line 141), closed at line 240. There
is no `{{- else }}` and no `fail` call anywhere in the chart's templates, and the chart ships no `values.schema.json`.

So a value that is neither of those two strings (a typo, a different case, or a mode name from another product)
renders the ConfigMap successfully with `otel.yaml: |` (line 8) followed by nothing. Helm reports success, the
ConfigMap is created, and the collector starts with `--config /conf/otel.yaml` pointing at an empty file.

The relevant literals all sit in `otelcol@v0.154.0/config.go`: `errors.New("empty configuration file")`,
`errors.New("no receiver configuration specified in config")`, and
`errors.New("no exporter configuration specified in config")`. Whichever applies is wrapped by
`fmt.Errorf("invalid configuration: %w", err)` (`otelcol@v0.154.0/collector.go`) before cobra prints it.

**How to check:**

1. Read the rendered config and confirm it is empty.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' | wc -c
   ```

   A healthy installation returns several thousand characters. A value near zero confirms this case.
2. Read the mode that was actually supplied.

   ```bash
   helm -n <namespace> get values <release-name> | grep OTEC_INSTALLATION_MODE
   ```

   The only two accepted values are `SPAN_METRICS_PROCESSOR` and `SENTRY_ENVELOPES_PROCESSING`, matched exactly and
   case-sensitively.

**How to fix:**

1. **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.** Re-apply with
   one of the two accepted values, spelled exactly.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set OTEC_INSTALLATION_MODE=SPAN_METRICS_PROCESSOR
   ```

**How to avoid this issue:**

Verify the ConfigMap is non-empty after any change to `OTEC_INSTALLATION_MODE`. Nothing in the chart validates this
value, so a typo is caught only by the collector at startup.

**Data to collect:**

* The byte count of the `otel.yaml` key.
* The `OTEC_INSTALLATION_MODE` value from `helm get values`.
* The previous container's log in full.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:8`, `:12`, `:141`, `:240`;
  `collector/main.go:51-55`; `otelcol@v0.154.0/config.go`, `collector.go`.

### Collector will not start, reporting an exporter that is not configured

**Symptoms:**

* The pod enters `CrashLoopBackOff` immediately after a `helm upgrade` that set `OTEC_ENABLE_LOGGING: false`.
* The previous container's log holds one line naming an exporter the operator never removed, of the form
  `Error: invalid configuration: service::pipelines::logs: references exporter "graylogexporter" which is not
  configured`.
* The installation worked before the change, and no other value was touched.

**Root cause:**

The exporter definition and the pipeline that references it are gated on two different values. The `graylogexporter`
block is emitted only when logging is enabled
(`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:44-50`):

```yaml
    {{- if .Values.OTEC_ENABLE_LOGGING | default false }}
      graylogexporter:
        endpoint: {{ .Values.GRAYLOG_COLLECTOR_HOST }}:{{ .Values.GRAYLOG_COLLECTOR_PORT }}
```

The logs pipeline, however, tests `OTEC_ENABLE_LOGDEDUP_PROCESSOR` first and references the exporter unconditionally in
that branch (lines 121-130):

```yaml
        logs:
          {{- if .Values.OTEC_ENABLE_LOGDEDUP_PROCESSOR }}
          receivers: [otlp]
          processors: [logdedup, batch]
          exporters: [graylogexporter]
          {{- else if .Values.OTEC_ENABLE_LOGGING | default false }}
```

Both default to `true` in `values.yaml` (lines 305 and 310), so a default install is fine. Disabling logging *alone*
leaves the pipeline naming an exporter that was never defined.

The collector rejects this at config validation with
`"service::pipelines::%s: references exporter %q which is not configured"`
(`otelcol@v0.154.0/config.go`), wrapped by `fmt.Errorf("invalid configuration: %w", err)` (`collector.go`).

A second defect sits in the same block: with *both* flags false, `logs:` renders as a bare key with no children rather
than being omitted. That produces a pipeline with no receivers and no exporters, which fails with
`errors.New("must have at least one receiver")` or `errors.New("must have at least one exporter")`
(`service@v0.154.0/pipelines/config.go`).

**How to check:**

1. Read the failing line.

   ```bash
   kubectl -n <namespace> logs <pod-name> --previous --tail=20 | grep 'which is not configured'
   ```

2. Confirm the two flags disagree.

   ```bash
   helm -n <namespace> get values <release-name> | grep -E 'OTEC_ENABLE_LOGGING|OTEC_ENABLE_LOGDEDUP_PROCESSOR'
   ```

   `OTEC_ENABLE_LOGGING: false` alongside `OTEC_ENABLE_LOGDEDUP_PROCESSOR: true`, or the latter simply absent since
   it defaults to true, confirms this case.

**How to fix:**

1. Do not disable both flags in the generated configuration. That combination renders a bare `logs:` pipeline, which
   also fails validation.
2. Supply a full `CONFIG_MAP` that omits both `graylogexporter` and the logs pipeline. Carry over every component used
   by the remaining pipelines, including `health_check`.
3. **DANGEROUS — replacing `CONFIG_MAP` starts a rollout and a mistake can keep the replacement pod in
   `CrashLoopBackOff`. Review *Rollout and restart risk*, render the chart first, and keep the old pod available while
   the replacement becomes ready.**

   ```bash
   helm template <release-name> <chart-path> -f <values-file-without-logs-pipeline>
   helm -n <namespace> upgrade <release-name> <chart-path> -f <values-file-without-logs-pipeline>
   ```

**How to avoid this issue:**

Do not use either flag as a supported switch for removing the logs pipeline. Verify the rendered `service.pipelines`
block whenever either value changes.

**Data to collect:**

* The previous container's log line naming the unconfigured exporter.
* Both flag values from `helm get values`.
* The `service.pipelines.logs` block from the rendered `otel.yaml`.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:44-50`, `:121-130`;
  `charts/open-telemetry-collector/values.yaml:305`, `:310`; `otelcol@v0.154.0/config.go`;
  `service@v0.154.0/pipelines/config.go`.

### Collector rejects the transform processor config over a key nobody set there

**Symptoms:**

* The pod enters `CrashLoopBackOff` after a `helm upgrade` that configured tail-based sampling.
* The previous container's log reports invalid keys on the transform processor, containing the substring
  `has invalid keys:` and naming `policies`.
* The error mentions neither tail sampling nor the value that was actually changed, so it appears unrelated to the
  edit.
* Tail sampling was expected to be enabled and is absent from the running pipeline.

**Root cause:**

The two tail-sampling values are spliced in as separate blocks at different indentation levels, and the second depends
positionally on the first (`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:72-77`):

```yaml
    {{- if .Values.OTEC_TAILBASED_GENERAL_PARAMETERS}}
      {{- toYaml (.Values.OTEC_TAILBASED_GENERAL_PARAMETERS) | nindent 6 }}
    {{- end }}
    {{- if .Values.OTEC_TAILBASED_POLICIES_PARAMETERS}}
      {{- toYaml (.Values.OTEC_TAILBASED_POLICIES_PARAMETERS) | nindent 8 }}
    {{- end }}
```

`OTEC_TAILBASED_GENERAL_PARAMETERS` supplies the `tail_sampling:` key itself at `nindent 6`, and
`OTEC_TAILBASED_POLICIES_PARAMETERS` supplies `policies:` one level deeper at `nindent 8` so it lands inside it. The
splice works only when both halves render.

Setting the policies without the general block never emits the `tail_sampling:` parent. The last content rendered
before it is the transform processor's statement list, and `policies:` at indent 8 is exactly the indentation of
`transform`'s own keys. The result is valid YAML that parses as `policies` being a key *of the transform processor*:

```yaml
      transform:
        error_mode: ignore
        trace_statements:
          - context: span
            statements:
              - truncate_all(resource.attributes, 256)
        policies:
          - name: composite-policy
```

The collector unmarshals component configuration strictly, so it rejects `policies` as an unknown key on `transform`.
The message shape is `'<path>' has invalid keys: <keys>`, produced by mapstructure
(`github.com/go-viper/mapstructure/v2`) under the header
`decoding failed due to the following error(s):` and wrapped by
`fmt.Errorf("cannot unmarshal the configuration: %w", err)` (`otelcol@v0.154.0/configprovider.go`).

Compounding the confusion, the pipeline wiring keys off the *general* parameters (lines 108-112), so `tail_sampling`
is correctly absent from the processor list. Nothing in the error mentions sampling at all.

**How to check:**

1. Read the failing line.

   ```bash
   kubectl -n <namespace> logs <pod-name> --previous --tail=30 | grep 'has invalid keys'
   ```

2. Confirm which of the two values is set.

   ```bash
   helm -n <namespace> get values <release-name> | grep -E 'OTEC_TAILBASED_(GENERAL|POLICIES)_PARAMETERS'
   ```

   Policies present without general parameters confirms this case.
3. Read the rendered processors block and observe where `policies:` landed.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -B15 'policies:'
   ```

   Healthy output shows `policies:` nested under `tail_sampling:`, not under `transform:`.

**How to fix:**

1. **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.** Set both values
   together. The general block carries the `tail_sampling` key and must be present whenever policies are.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <values-file-setting-both>
   ```

   The chart's own `values.yaml` shows the expected shape of each: `OTEC_TAILBASED_GENERAL_PARAMETERS` is an object
   whose single key is `tail_sampling`, and `OTEC_TAILBASED_POLICIES_PARAMETERS` is an object whose single key is
   `policies`.

**How to avoid this issue:**

Never set one of these two values without the other, and confirm after every change that the rendered `policies:` sits
under `tail_sampling:`. Note also that both are honored only in `SPAN_METRICS_PROCESSOR` mode; in
`SENTRY_ENVELOPES_PROCESSING` mode they are ignored entirely and sampling silently does not happen.

**Data to collect:**

* The previous container's log line containing `has invalid keys`.
* Both tail-sampling values from `helm get values`.
* The `processors` block from the rendered `otel.yaml`.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:72-77`, `:108-112`;
  `charts/open-telemetry-collector/values.yaml:69-70`, `:82-83`; `otelcol@v0.154.0/configprovider.go`;
  `github.com/go-viper/mapstructure/v2`.

## Helm chart and Kubernetes deployment

### Pod is OOMKilled with exit code 137 and no error in the collector log

**Symptoms:**

* `kubectl describe pod` shows `Last State: Terminated`, `Reason: OOMKilled`, `Exit Code: 137`.
* The collector log stops abruptly with no error and no shutdown message.
* The restart count climbs, and the pattern repeats under load.
* Or, before any kill, memory climbs steadily over days and the collector slows down as garbage collection consumes
  a growing share of CPU.
* The pod may also sit `Pending` with a scheduling failure mentioning insufficient memory, or be evicted when the
  node comes under memory pressure.

**Root cause:**

The container exceeded its configured memory limit and the kernel killed it immediately. The chart default is
2000Mi. There is no graceful shutdown, flush, or collector warning, which is why the log stops rather than ending with
an error.

This deployment has nothing that refuses data as memory fills. `memorylimiterextension` is compiled into the binary
(`builder-config.yaml:24`) but the rendered configuration does not enable it: `service.extensions` lists only
`[health_check, pprof]`, and the `memory_limiter` *processor* is not part of this build at all. `GOMEMLIMIT` is
not set anywhere in the Dockerfile or the chart, and the chart offers no value for setting arbitrary environment
variables, so the Go runtime has no heap target either.

The usual driver is the tail sampler, which holds every in-flight trace in memory until its decision, so memory scales
with throughput and with `num_traces`.

The shipped resource shape makes the scheduling variants worse: the chart requests 100Mi against a 2000Mi limit, a
twentyfold gap. Kubernetes schedules against the request, so the pod can be placed on a node with almost no
headroom, and a pod consuming far above its request is an early eviction candidate under node pressure.

**How to check:**

1. Confirm the kill reason, which distinguishes this from a configuration failure or a probe kill.

   ```bash
   kubectl -n <namespace> describe pod <pod-name> | grep -A6 -E 'Last State|Requests|Limits|Events'
   ```

   `Reason: OOMKilled` with `Exit Code: 137` confirms the kill. `Exit Code: 1` points at
   *Collector startup and configuration* instead. A `FailedScheduling` event mentioning insufficient memory points
   at the request-versus-limit gap rather than at the collector.
2. Compare current usage against the request and the limit.

   ```bash
   kubectl -n <namespace> top pod <pod-name>
   ```

3. Where memory grows slowly rather than spiking, read a heap profile from the pprof endpoint the chart exposes on
   port 1777. This reads state and changes nothing.

   ```bash
   kubectl -n <namespace> port-forward <pod-name> 1777:1777 &
   go tool pprof -top http://localhost:1777/debug/pprof/heap
   ```

   `tailsamplingprocessor` frames dominating the profile confirm the sampler as the driver.

**How to fix:**

1. **DANGEROUS — this change starts a rollout and raising the limit can move memory pressure to the node. Review
   *Rollout and restart risk* before applying it.** Raise the memory limit to match observed peak usage, and raise the
   request with it so the scheduler reserves what the collector actually uses.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set resources.requests.memory=1000Mi --set resources.limits.memory=4000Mi
   ```

   Do not set the entire `resources` object to `null`. See *Helm upgrade fails to render the chart with a nil pointer
   error*.
2. Reduce what the collector holds, rather than only granting it more room: lower `num_traces` and `decision_wait`
   on tail sampling. Read *Traces are incomplete, or fewer of them arrive than expected* first, because those
   values trade memory against dropped traces.
3. **DANGEROUS — restarting the only ready pod interrupts ingestion until a replacement is ready. It also discards
   tail-sampling state and anything held in in-memory exporter queues.** Where memory has already accumulated and the
   pod is near its limit, roll it to reclaim the memory. This buys time and does not address the growth.
   The Deployment is named after `SERVICE_NAME`, which defaults to `qubership-open-telemetry-collector`.

   ```bash
   kubectl -n <namespace> rollout restart deployment/<SERVICE_NAME>
   ```

4. `memorylimiterextension` can reject requests before standard OTLP HTTP and gRPC receivers decode them, but declaring
   the extension alone does nothing. A custom `CONFIG_MAP` must define it, list it under `service.extensions`, and add
   its middleware ID to each supported receiver protocol. Carry over every other component, including `health_check`.
   Treat this as a designed pipeline change, not a quick fix.

**How to avoid this issue:**

Alert on container working set against the limit. Nothing inside this deployment refuses data as memory fills, so
the only warning available is the external one.

**Data to collect:**

* The `Last State` block from `kubectl describe pod`.
* Memory usage trend across the hours before the kill.
* The `tail_sampling` block from the rendered `otel.yaml`.

<!-- markdownlint-disable line-length -->
**Sources:**

* [How to Fix OpenTelemetry Collector OOM Killed Errors in Kubernetes (OneUptime)](https://oneuptime.com/blog/post/2026-02-06-fix-opentelemetry-collector-oom-killed-kubernetes/view)

* Code analysis: `builder-config.yaml:24`;
  `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:94-100`;
  `charts/open-telemetry-collector/values.yaml`.
<!-- markdownlint-enable line-length -->

### Helm upgrade fails to render the chart with a nil pointer error

**Symptoms:**

* `helm upgrade` or `helm template` fails before anything reaches the cluster, with a template error about a nil
  pointer evaluating an interface.
* The error appears immediately after explicitly removing the `resources` object, for example with
  `--set resources=null`.
* The previously installed release is untouched, since the failure happens at render time.

**Root cause:**

The Deployment template tests `.Values.resources` and then dereferences the same object in its `else` branch
(`charts/open-telemetry-collector/templates/deployment.yaml:134-144`). Setting `resources` to `null` therefore selects
the branch that reads `.Values.resources.limits.cpu` and fails before Kubernetes sees the manifests.

The HPA template also dereferences `.Values.resources.limits.cpu` and `.Values.resources.requests.cpu` on every render
(`HorizontalPodAutoscaler.yaml:30`). Helm normally merges `resources: {}` and partial resource overrides with the chart
defaults, so those common forms keep the CPU values and render successfully. Explicitly removing the object is the
reproducible failure.

**How to check:**

1. Reproduce the failure without touching the cluster.

   ```bash
   helm template <release-name> <chart-path> -f <your-values-file>
   ```

   Rendering is read-only, so this is safe to run against the values you intend to apply.
2. Read the effective values and confirm that `resources` was not removed.

   ```bash
   helm -n <namespace> get values <release-name> --all | grep -A6 '^resources:'
   ```

**How to fix:**

1. Remove `resources=null` from the command or values input. Restore the chart defaults, or supply a complete resource
   object when you intentionally replace it.

   ```bash
   helm template <release-name> <chart-path> --set resources.requests.cpu=100m --set resources.limits.cpu=2000m
   ```

2. Once `helm template` renders cleanly, apply the same values. **DANGEROUS — this change starts a rollout. Review
   *Rollout and restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <your-values-file>
   ```

**How to avoid this issue:**

Run `helm template` with your values before every upgrade. The chart has no `values.schema.json`, and CI does not
lint the rendered manifests: `.github/super-linter.env` sets `VALIDATE_KUBERNETES_KUBECONFORM=false`, so rendering
is the only validation available.

**Data to collect:**

* The full `helm template` error output.
* The `resources` block from your values file.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/HorizontalPodAutoscaler.yaml:30`;
  `charts/open-telemetry-collector/templates/deployment.yaml:137-144`; `.github/super-linter.env`.

### Pod never becomes ready after replacing CONFIG_MAP

**Symptoms:**

* The pod starts but never becomes `Ready` after a change that supplied a custom `CONFIG_MAP`.
* `kubectl describe pod` reports repeated liveness and readiness probe failures on port 13133.
* The collector may restart after three consecutive liveness failures even though its main process did not report a
  configuration error.

**Root cause:**

`CONFIG_MAP` replaces the generated collector configuration instead of merging with it. If the replacement omits the
`health_check` extension, omits it from `service.extensions`, or binds it to a port other than
`OTEC_HEALTH_CHECK_PORT`, both Kubernetes probes fail.

The liveness probe has no `initialDelaySeconds` or startup probe
(`charts/open-telemetry-collector/templates/deployment.yaml:165-183`). This makes a permanently missing health endpoint
restart quickly, but the missing or mismatched extension is the defect. Tail sampling's `decision_wait` does not delay
collector startup.

Port 13133 is absent from the container's declared ports and from the Service. Probes still work because the kubelet
connects directly to the pod IP, but the health endpoint is not reachable through the Service.

**How to check:**

1. Distinguish a probe kill from a collector configuration exit.

   ```bash
   kubectl -n <namespace> describe pod <pod-name> | grep -E 'Liveness|Readiness|Last State|Exit Code|Reason'
   kubectl -n <namespace> logs <pod-name> --previous --tail=50
   ```

   `Exit Code: 1` with an `Error:` line indicates a startup validation failure. Repeated probe failures without that
   error point to the health endpoint.
2. Read both extension locations from the rendered configuration.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A5 -E '^extensions:|^  extensions:'
   ```

   The config must define `health_check` on `0.0.0.0:<OTEC_HEALTH_CHECK_PORT>` and list it under
   `service.extensions`.

**How to fix:**

1. Add the `health_check` definition and service reference to the custom `CONFIG_MAP`, using the same port as the
   probes.
2. Render the chart and inspect both blocks before applying it.
3. **DANGEROUS — this correction starts another rollout. A bad replacement config can leave both the failed and the
   replacement rollout without a ready pod. Review *Rollout and restart risk* before applying it.**

**How to avoid this issue:**

Treat `health_check` as mandatory whenever `CONFIG_MAP` replaces the generated configuration. Validate its endpoint
against `OTEC_HEALTH_CHECK_PORT` before every rollout.

**Data to collect:**

* The probe failure events from `kubectl describe pod`.
* The previous container's logs.
* The `extensions` and `service.extensions` blocks from the rendered `otel.yaml`.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/deployment.yaml:100-133`, `:165-183`;
  `charts/open-telemetry-collector/templates/metrics_collector_config.yaml`.

## Trace pipeline and tail sampling

Tail sampling is on by default: `charts/open-telemetry-collector/values.yaml` ships
`OTEC_TAILBASED_GENERAL_PARAMETERS` with `decision_wait: 31s`, `num_traces: 7500`, and
`expected_new_traces_per_sec: 200`, and the traces pipeline adds `tail_sampling` whenever that value is set. The
cases below therefore apply to a default installation, not only to a customized one.

### Traces are incomplete, or fewer of them arrive than expected

**Symptoms:**

* Traces in Jaeger are incomplete: the root span is present but child spans are absent, or the reverse.
* Or whole traces are missing, and the volume in Jaeger is below what the applications report sending.
* No error or drop message appears in the collector logs in either case.
* The problem worsens as trace volume rises, and is worst during bursts.

**Root cause:**

Both symptoms come from the same sizing constraint, and which one you see depends on the timing.

The tail sampler groups spans by trace ID and holds at most `num_traces` traces in a circular buffer while it waits
`decision_wait` before deciding.

1. **Whole traces missing.** When a new trace arrives and the buffer is full, the oldest is evicted, which can drop
   a trace before it has ever been sampled. The shipped sizing is tight: at `expected_new_traces_per_sec: 200` and
   `decision_wait: 31s`, roughly 6200 traces must be held just to cover the wait window, against a `num_traces` of
   7500. Any burst above the expected rate evicts undecided traces, silently.
2. **Traces incomplete.** Spans arriving after their trace's decision are "late". The chart's default
   `decision_cache` (`sampled_cache_size: 75000`, `non_sampled_cache_size: 22500`) replays the original verdict for
   a late span whose trace ID is still cached, so on a default install this case starts only once the ID has also
   been evicted from the cache. The processor then treats the late span as a brand-new trace and makes a fresh
   decision, which may differ from the first. Different parts of one trace then get different verdicts.

**How to check:**

1. Read the removal-age histogram, which helps separate the two causes.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8888/metrics \
     | grep tail_sampling_sampling_trace_removal_age
   ```

   This is a cumulative Prometheus histogram, not one age value. Compare the rate of observations in buckets below
   `decision_wait` with `_count`. A significant share below the wait time indicates early eviction. Observations near
   or above the wait time are normal and do not by themselves prove incomplete traces.
2. Read the late-span histogram.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8888/metrics \
     | grep tail_sampling_sampling_late_span_age
   ```

   Use the histogram's `_count` rate to measure whether late spans are arriving. A rising count confirms late arrivals,
   but correlate it with missing trace parts before attributing the user-visible symptom to sampling.
3. Read the configured sizing and do the arithmetic.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A6 'tail_sampling:'
   ```

   Compare `num_traces` against `expected_new_traces_per_sec` multiplied by `decision_wait`. A ratio below about
   1.5 leaves no burst headroom.

**How to fix:**

1. Confirm the decision caches are configured, so a "keep" verdict survives after the spans leave memory. The chart
   ships `sampled_cache_size: 75000` and `non_sampled_cache_size: 22500` under `decision_cache` in
   `OTEC_TAILBASED_GENERAL_PARAMETERS`; verify they are still present in the rendered config before changing
   anything else.
2. **DANGEROUS — this change starts a rollout and increases memory use, which can cause an OOM kill or node pressure.
   Review *Rollout and restart risk* before applying it.** Raise `num_traces` to cover peak throughput. No memory guard
   is enabled in this deployment.
   See *Pod is OOMKilled with exit code 137 and no error in the collector log* before raising it far.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set OTEC_TAILBASED_GENERAL_PARAMETERS.tail_sampling.num_traces=15000
   ```

3. Lowering `decision_wait` also clears the buffer faster, but it trades one symptom for the other: a shorter wait
   means more spans arrive after their decision. Change it only when raising `num_traces` is not possible.

**How to avoid this issue:**

Size `num_traces` for peak rather than average throughput, keeping it comfortably above
`expected_new_traces_per_sec * decision_wait`, and alert on both the removal-age and late-span metrics.

**Data to collect:**

* The `tail_sampling` block from the rendered `otel.yaml`.
* Two `:8888/metrics` scrapes filtered to `tail_sampling`, taken far enough apart to calculate rates.

<!-- markdownlint-disable line-length -->
**Sources:**

* [tailsamplingprocessor at v0.154.0](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.154.0/processor/tailsamplingprocessor)
<!-- markdownlint-enable line-length -->

## Export to backends and data loss

### OTLP exporter logs "Exporting failed. Dropping data." with a sending queue error

**Symptoms:**

* Repeated log lines reading `Exporting failed. Dropping data.` carrying the error `sending queue is full`.
* Often preceded by `Exporting failed. Will retry the request after interval.`
* Traces are missing in Jaeger while the collector keeps running and stays `Ready`.
* `otelcol_exporter_enqueue_failed_spans` is rising with `exporter="otlp"`.

**Root cause:**

The standard OTLP exporter enables a bounded in-memory sending queue by default. In v0.154.0 it holds 1000 requests
and uses 10 consumers. When Jaeger is slow or unreachable, consumers spend time retrying and the queue stops draining
fast enough. Once it is full, new items are rejected and counted by `otelcol_exporter_enqueue_failed_spans`.

The chart does not override `sending_queue` or `retry_on_failure`, and it configures no `file_storage` extension. The
OTLP exporter therefore uses its built-in retry and queue defaults, and nothing in that queue survives a restart.
The custom Graylog exporters use a different internal queue and different log messages; see the Graylog section.

The exact strings come from the exporter helper (`exporterhelper@v0.154.0`): `"Exporting failed. Dropping data."`
(`internal/queue_sender.go`), `"Exporting failed. Will retry the request after interval."`
(`internal/retry_sender.go`), and the error value `"sending queue is full"` (`internal/queue/queue.go`).

**How to check:**

1. Find the drop lines.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=1000 | grep 'Exporting failed'
   ```

2. Read the OTLP exporter's queue counters, which record loss that produces no log line.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8888/metrics \
     | grep -E 'otelcol_exporter_(enqueue_failed|send_failed|queue_size|queue_capacity)'
   ```

   Filter the result to `exporter="otlp"`. A rising `enqueue_failed` count is loss. `queue_size` at `queue_capacity`
   confirms saturation.
3. Compare what came in against what went out.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8888/metrics \
     | grep -E 'otelcol_(receiver_accepted|exporter_sent)_spans'
   ```

**How to fix:**

Fix the backend first. A full queue is almost always a symptom of a stalled destination rather than a sizing
mistake.

1. Confirm that the failing exporter is `otlp`, then diagnose Jaeger. See *OTLP export to Jaeger fails and traces never
   arrive*.
2. Where the backend is healthy and the queue only saturates during bursts, enlarge it. The chart exposes no value
   for exporter queue settings, so this requires replacing the pipeline config through `CONFIG_MAP`.
   **DANGEROUS — this starts a rollout, and increasing an in-memory queue raises the collector's memory requirement.
   It also increases the amount lost if the pod terminates. Review *Rollout and restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <values-file-with-custom-CONFIG_MAP>
   ```

   Supplying `CONFIG_MAP` replaces the entire pipeline definition, so carry over every receiver, processor,
   exporter, and the `health_check` extension. Omitting the extension makes both probes fail permanently.

**How to avoid this issue:**

Alert on `otelcol_exporter_enqueue_failed_spans{exporter="otlp"}` rather than on log lines alone; check the exact
metric name on a live `:8888/metrics` scrape first, since the Prometheus exposition can append `_total` to counters.
Size the queue for
the outage window you are willing to absorb and for the pod's memory limit. No persistent queue is configured, so a
restart discards whatever is queued regardless of size.

**Data to collect:**

* The `Exporting failed` log lines with timestamps.
* The exporter counters from `:8888`.
* The backend's own logs for the same window.

<!-- markdownlint-disable line-length -->
**Sources:**

* [Batching, Queuing, and Retries in the OpenTelemetry Collector (Dash0)](https://www.dash0.com/guides/batching-queuing-and-retries-in-the-opentelemetry-collector)

* [Resiliency (OpenTelemetry documentation)](https://opentelemetry.io/docs/collector/resiliency/)
* Code analysis: `exporterhelper@v0.154.0/internal/queue_sender.go`, `internal/retry_sender.go`,
  `internal/queue/queue.go`.
<!-- markdownlint-enable line-length -->

### OTLP export to Jaeger fails and traces never arrive

**Symptoms:**

* Repeated log lines containing one of:
  `connection error: desc = "transport: Error while dialing: dial tcp <ip>:4317: connect: connection refused"`,
  `lookup <host>: no such host`, or
  `transport: authentication handshake failed: tls: first record does not look like a TLS handshake`.
* Followed by `Exporting failed. Will retry the request after interval.`
* Traces never appear in Jaeger while metrics derived from the same spans still reach Prometheus.

**Root cause:**

The OTLP exporter cannot deliver to `JAEGER_COLLECTOR_HOST:JAEGER_COLLECTOR_OTLP_PORT`. The log line tells you
which of four causes applies, listed here in the order they usually turn out to be it.

1. **Connection refused.** The Jaeger collector is down, or its OTLP receiver is not enabled.
2. **Connection refused, wrong port.** A Jaeger collector's OTLP gRPC port is 4317; its legacy Jaeger gRPC port is
   14250. The chart defaults `JAEGER_COLLECTOR_OTLP_PORT` to 4317 and only ever emits that value on the exporter,
   while `JAEGER_COLLECTOR_PORT` (default 14250) is documented but never read.
3. **No such host.** DNS resolution fails inside the cluster: a short service name that does not resolve from the
   collector's namespace, a typo, or unhealthy cluster DNS. A short name resolves only within the same namespace,
   so a Jaeger in another namespace needs the fully qualified name.
4. **TLS handshake failure.** Both OTLP exporters hardcode `tls: insecure: true` in the chart template, so they
   always speak plaintext. If the Jaeger endpoint requires TLS, the plaintext bytes are rejected. No chart value
   enables TLS on the exporter.

Note that the host silently falls back: `JAEGER_COLLECTOR_HOST`, then `TRACING_HOST`, then the hardcoded
`jaeger-app-collector.jaeger`. A typo produces no error at install time, only this connection failure.

**How to check:**

1. Read the endpoint the exporter was given.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A4 'otlp:' | grep endpoint
   ```

2. Test reachability from inside the pod.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- nc -zv <jaeger-host> 4317
   ```

3. Confirm the Jaeger Service exposes an OTLP gRPC port rather than only 14250.

   ```bash
   kubectl -n <namespace> get svc <jaeger-collector-service> -o jsonpath='{.spec.ports[*].port}'
   ```

4. For the no-such-host variant, resolve the name from inside the pod, and confirm cluster DNS is healthy.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- nslookup <jaeger-host>
   kubectl -n kube-system get pods -l k8s-app=kube-dns
   ```

**How to fix:**

1. Where the Jaeger collector is down or its OTLP receiver is disabled, fix that first. The collector retries on its
   own and needs no restart once the destination returns. Where cluster DNS is unhealthy, fix that first too; the
   collector is a symptom, not the cause.
2. **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.** Where the host
   or port is wrong, correct both in one upgrade, using the fully qualified service name.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set JAEGER_COLLECTOR_HOST=<jaeger-collector>.<jaeger-namespace>.svc.cluster.local \
     --set JAEGER_COLLECTOR_OTLP_PORT=4317
   ```

3. For the TLS variant, point the exporter at an endpoint that accepts plaintext gRPC, or place a proxy inside the
   cluster that terminates TLS toward Jaeger and accepts plaintext from the collector. This keeps the chart
   unmodified. Reaching a TLS-only backend directly requires supplying a full `CONFIG_MAP` with corrected `tls`
   settings, carrying over every receiver, processor, exporter, and the `health_check` extension.

**How to avoid this issue:**

Set `JAEGER_COLLECTOR_HOST` explicitly to a fully qualified service name rather than relying on the silent fallback
chain, and use the OTLP port, not the legacy 14250. Ignore `JAEGER_COLLECTOR_PORT`, which the pipeline config never
reads. Treat plaintext OTLP as a fixed property of this chart when planning the trace backend.

**Data to collect:**

* The rendered `otlp` exporter block.
* The dial, resolution, or handshake error line in full.
* The Jaeger Service definition and its TLS configuration.

<!-- markdownlint-disable line-length -->
**Sources:**

* [OpenTelemetry Collector Under the Hood: Backpressure (Axoflow)](https://axoflow.com/blog/opentelemetry-controller-outages-pipelines-backpressure)

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml`;
  `charts/open-telemetry-collector/values.yaml`; `docs/installation-notes.md:53`, `:57`.
<!-- markdownlint-enable line-length -->

## Span metrics and Prometheus

### Span metrics stop covering new operations and an overflow series appears

**Symptoms:**

* Newly deployed services or endpoints never appear in the span-metric dashboards, while existing ones keep updating.
* A series tagged `otel.metric.overflow="true"` is present in the metrics backend.
* The `:8889/metrics` payload stops growing even as traffic diversifies.

**Root cause:**

The connector tracks one time series per unique combination of dimensions. This chart caps that at 5000
(`aggregation_cardinality_limit: 5000` in
`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:91`), which is a deliberate circuit
breaker: upstream, the setting defaults to `0`, meaning no limit at all. Once the limit is reached, further unique
combinations are dropped and folded into a single entry marked `otel.metric.overflow="true"`.

The limit is usually reached because span names carry identifiers. The chart's dimensions are
`destinationPageUrl`, `error`, and `spanStatus`, and `destinationPageUrl` in particular multiplies with every
distinct URL. Span names like `GET /product/1YMWWN1N4O` produce one series each.

Reaching the cap is a protection working as intended, not a failure. The failure is the cardinality that made it
necessary.

**How to check:**

1. Look for the overflow series, which confirms the cap has been reached.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8889/metrics | grep 'otel.metric.overflow'
   ```

2. Gauge the series count against the 5000 limit.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8889/metrics | grep -c '^traces_span_metrics'
   ```

3. Find which span names are multiplying, by grouping on `span_name` in the metrics backend.

**How to fix:**

Reduce cardinality at the source rather than raising the cap; a higher cap moves the memory cost to the collector
and the backend.

1. Extend the existing `transformprocessor` OTTL statements to collapse identifiers in span names before the connector
   sees them. The shipped transform limits and truncates attributes; it does not normalize URI paths or span names.
   Upstream recommends `set_semconv_span_name()` as a short-term normalization option before the connector.
   **DANGEROUS — this changes metric identity, starts a rollout, and can merge previously distinct series. Validate
   the resulting labels and review *Rollout and restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <values-file-with-custom-CONFIG_MAP>
   ```

   The transform statements are part of the pipeline config, so changing them means supplying a full `CONFIG_MAP`.
   Carry over every receiver, processor, exporter, and the `health_check` extension.
2. Fix the instrumentation so span names follow the HTTP semantic conventions and carry a route template rather
   than a concrete path. Upstream calls this the ideal long-term solution.

**How to avoid this issue:**

Keep identifiers out of span names at the instrumentation layer, and treat the appearance of an
`otel.metric.overflow` series as an alert rather than a curiosity.

**Data to collect:**

* The overflow series and the total span-metric series count from `:8889`.
* The top span names by series count from the backend.
* The `spanmetrics` and `transform` blocks from the rendered `otel.yaml`.

<!-- markdownlint-disable line-length -->
**Sources:**

* [spanmetricsconnector README at v0.154.0](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.154.0/connector/spanmetricsconnector/README.md)

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:79-93`.
<!-- markdownlint-enable line-length -->

### Span metrics disappear from the backend a few minutes after traffic stops

**Symptoms:**

* A span metric present on `:8889` vanishes roughly five minutes after the last matching span, while the collector
  stays up.
* Metrics for low-traffic endpoints flicker in and out.
* Dashboards show gaps for quiet services rather than a flat line.

**Root cause:**

The Prometheus exporter stops exposing a series that has not been updated within `metric_expiration`, which
defaults to `5m`.

Which mode you run decides whether this applies. In `SENTRY_ENVELOPES_PROCESSING` the chart sets
`metric_expiration: 336h` on the exporter
(`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:152`), so series survive two weeks. In
the default `SPAN_METRICS_PROCESSOR` mode the exporter block carries only `endpoint: ":8889"` (line 31-32) and the
5 minute default applies.

**How to check:**

1. Confirm which mode is running and whether the exporter sets an expiration.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A3 'prometheus:'
   ```

   An exporter block with no `metric_expiration` runs on the 5 minute default.
2. Observe a quiet series disappearing, using existing traffic rather than generating any.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- wget -qO- localhost:8889/metrics | grep <metric-name>
   ```

   Repeat after five idle minutes for that endpoint. Disappearance confirms expiry.

**How to fix:**

1. **DANGEROUS — this starts a rollout and retains more series in collector memory. A large expiration can contribute
   to an OOM kill. Review *Rollout and restart risk* before applying it.** Set `metric_expiration` on the Prometheus
   exporter above the longest expected quiet period. The chart exposes no value for it in
   `SPAN_METRICS_PROCESSOR` mode, so this requires a full `CONFIG_MAP` carrying every receiver, processor,
   exporter, and the `health_check` extension.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <values-file-with-custom-CONFIG_MAP>
   ```

**How to avoid this issue:**

Size `metric_expiration` for the quietest endpoint you care about, and prefer backend-side handling of staleness
over long expirations, since every retained series costs collector memory.

**Data to collect:**

* The `prometheus` exporter block from the rendered `otel.yaml`.
* Two `:8889/metrics` scrapes taken more than five minutes apart across a quiet period.

<!-- markdownlint-disable line-length -->
**Sources:**

* [prometheusexporter README at v0.154.0](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.154.0/exporter/prometheusexporter/README.md)

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:31-32`, `:150-152`.
<!-- markdownlint-enable line-length -->

### Prometheus has no target for the collector, or shows it as down

**Symptoms:**

* The collector does not appear among Prometheus targets at all.
* Or it appears and reports `DOWN` with a connection error.
* Scraping `:8889/metrics` and `:8888/metrics` by hand from inside the cluster works.

**Root cause:**

Several causes produce the same result, in roughly this order of frequency.

1. The ServiceMonitor was never created. The chart renders it only when `MONITORING_ENABLED` is true, and that
   value defaults to `false` in `charts/open-telemetry-collector/values.yaml`.
2. The Service port names no longer match. The ServiceMonitor references ports by name, namely
   `SERVICE_MONITOR_PORT_NAME` (default `exporter-prom`) and `self-telemetry`, while the Service defines those
   names with ports 8889 and 8888.
   Overriding `SERVICE_PORTS` replaces the port list wholesale, so a custom list that omits or renames either port
   leaves the ServiceMonitor pointing at nothing. A ServiceMonitor whose named port does not exist yields zero
   targets and no error.
3. The Prometheus instance's `serviceMonitorSelector` does not match the ServiceMonitor's labels, so it is never
   adopted.
4. The Prometheus ServiceAccount lacks RBAC to list Services and Endpoints in this namespace.

**How to check:**

1. Confirm the ServiceMonitor exists.

   ```bash
   kubectl -n <namespace> get servicemonitor
   ```

   Nothing listed means `MONITORING_ENABLED` is false and no other cause applies.
2. Compare the port names the ServiceMonitor asks for against the ones the Service defines.

   ```bash
   kubectl -n <namespace> get servicemonitor <name> -o jsonpath='{.spec.endpoints[*].port}'
   kubectl -n <namespace> get svc <SERVICE_NAME> -o jsonpath='{.spec.ports[*].name}'
   ```

   Every name in the first output must appear in the second.
3. Read the target's error in the Prometheus UI under Status then Targets.

**How to fix:**

1. Where the ServiceMonitor is absent, enable it. This value does not change the pod template, so changing only
   `MONITORING_ENABLED` does not normally restart the collector.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values --set MONITORING_ENABLED=true
   ```

2. Where a custom `SERVICE_PORTS` dropped or renamed a port, restore the `exporter-prom` and `self-telemetry`
   names, or set `SERVICE_MONITOR_PORT_NAME` to the name you actually use.
3. Where the selector or RBAC is at fault, fix them on the Prometheus side. Neither is configurable from this
   chart.

**How to avoid this issue:**

Treat `SERVICE_PORTS` as replacing the entire port list rather than merging into it, and verify the target appears
in Prometheus after any change to it or to `SERVICE_MONITOR_PORT_NAME`.

**Data to collect:**

* The ServiceMonitor and Service definitions.
* The target's error text from the Prometheus UI.
* The Prometheus `serviceMonitorSelector` configuration.

<!-- markdownlint-disable line-length -->
**Sources:**

* [ServiceMonitor endpoints port and targetPort ambiguity (prometheus-operator issue 2515)](https://github.com/prometheus-operator/prometheus-operator/issues/2515)

* [Prometheus Operator troubleshooting](https://prometheus-operator.dev/docs/platform/troubleshooting/)
* Code analysis: `charts/open-telemetry-collector/templates/service-monitor.yaml:13`, `:17`;
  `charts/open-telemetry-collector/templates/service.yaml`.
<!-- markdownlint-enable line-length -->

## Sentry envelope receiver

### Helm cannot render Sentry mode when the Graylog host is absent

**Symptoms:**

* `helm install`, `helm upgrade`, or `helm template` fails immediately after setting
  `OTEC_INSTALLATION_MODE=SENTRY_ENVELOPES_PROCESSING`.
* The error points at `metrics_collector_config.yaml:158` and reads `invalid value; expected string`.
* No Kubernetes resource is changed because rendering fails first.

**Root cause:**

The Sentry-mode `logtcpexporter` endpoint falls back from `GRAYLOG_COLLECTOR_HOST` to `GRAYLOG_UI_URL`. The chart does
not define `GRAYLOG_UI_URL` in `values.yaml`, and Helm's `replace` function rejects the resulting untyped nil value.
The default empty `GRAYLOG_COLLECTOR_HOST` therefore makes the Sentry-mode branch impossible to render with the chart
defaults.

Supplying `GRAYLOG_COLLECTOR_HOST` gets past this render failure but exposes a separate configuration defect described
in *Collector will not start in Sentry mode because Graylog keys use the wrong convention*.

**How to check:**

1. Reproduce the failure without touching the cluster.

   ```bash
   helm template <release-name> <chart-path> \
     --set OTEC_INSTALLATION_MODE=SENTRY_ENVELOPES_PROCESSING
   ```

2. Confirm that neither Graylog value supplies a usable fallback.

   ```bash
   helm -n <namespace> get values <release-name> --all \
     | grep -E 'GRAYLOG_(COLLECTOR_HOST|UI_URL)'
   ```

**How to fix:**

1. Do not apply the Sentry-mode values until `helm template` succeeds and the rendered collector configuration passes
   validation.
2. Fix the chart so it requires a non-empty `GRAYLOG_COLLECTOR_HOST` or handles an absent fallback explicitly. Until
   that change is available, use a reviewed full `CONFIG_MAP` that defines only the components used by Sentry mode.
3. **DANGEROUS — switching installation modes replaces the complete collector pipeline and starts a rollout. A bad
   replacement can stop both Sentry and OTLP ingestion. Review *Rollout and restart risk* before applying it.**

**How to avoid this issue:**

Render both installation modes in CI with their minimum supported values. A mode string being accepted by the template
condition does not mean that the selected branch is renderable with defaults.

**Data to collect:**

* The complete Helm render error.
* The effective Graylog host values.
* The values file used to select Sentry mode.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:157-162`;
  `charts/open-telemetry-collector/values.yaml`.

### Browser SDK requests fail with connection refused after customizing sentry receiver parameters

**Symptoms:**

* Sentry envelopes stop arriving immediately after a `helm upgrade` that set `OTEC_SENTRY_RECEIVER_PARAMETERS`.
* The browser SDK reports a network error or a connection refused against the collector's Ingress or Service.
* The collector pod is `Running` and `Ready`, and its logs show `SentryReceiver started` with no error afterward.
* Nothing is listening on port 8080 inside the pod, even though the Service and Ingress both target it.

**Root cause:**

The chart supplies the receiver's `endpoint` only in the fallback branch of the template. In
`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:143-148` the receiver block reads:

```yaml
      sentryreceiver:
      {{- if .Values.OTEC_SENTRY_RECEIVER_PARAMETERS }}
        {{- toYaml (.Values.OTEC_SENTRY_RECEIVER_PARAMETERS) | nindent 8 }}
      {{- else }}
        endpoint: ":8080"
      {{- end }}
```

Setting *any* key under `OTEC_SENTRY_RECEIVER_PARAMETERS`, even one unrelated to the endpoint such as
`level-evaluation-strategy`, skips the `else` branch entirely, so no `endpoint` is emitted. The receiver then falls
back to its compiled-in default, `defaultBindEndpoint = "0.0.0.0:9777"`
(`receiver/sentryreceiver/factory.go:30`).

Meanwhile the Deployment still declares `containerPort: 8080` named `sentry-receiver`
(`deployment.yaml:125-126`), the Service still targets that port name (`service.yaml:45-48`), and the Ingress still
routes to that Service port (`Ingress.yaml:27`). The collector is healthy and listening, just on a port nothing
routes to.

**How to check:**

1. Read the rendered receiver config and confirm whether `endpoint` is present at all.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A5 'sentryreceiver:'
   ```

   A healthy result shows an `endpoint` key. If the block lists only your custom parameters and no `endpoint`, this
   case applies.
2. Confirm the values you supplied.

   ```bash
   helm -n <namespace> get values <release-name> | grep -A10 OTEC_SENTRY_RECEIVER_PARAMETERS
   ```

   Any non-empty object here, without an `endpoint` key, triggers the failure.

**How to fix:**

1. **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.** Re-apply the
   values with `endpoint` restored alongside your custom parameters, so the receiver binds the port the Service
   targets.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set OTEC_SENTRY_RECEIVER_PARAMETERS.endpoint=":8080"
   ```

   Keep every other key you had already set. `--reuse-values` preserves them, but the `endpoint` key must be added
   explicitly because the chart no longer supplies it.

**How to avoid this issue:**

Treat `OTEC_SENTRY_RECEIVER_PARAMETERS` as a full replacement for the receiver block rather than an overlay. Whenever
you set it, include `endpoint: ":8080"` as the first key.

**Data to collect:**

* The `sentryreceiver` block from the `opentelemetry-collector-config` ConfigMap.
* The output of `helm get values` for the release.
* `kubectl -n <namespace> get svc <service-name> -o yaml`, showing the targeted port name.

**Sources:**

* Code analysis: `receiver/sentryreceiver/factory.go:30`,
  `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:143-148`,
  `charts/open-telemetry-collector/templates/deployment.yaml:125-126`,
  `charts/open-telemetry-collector/templates/service.yaml:45-48`.

### Every Sentry envelope is rejected with HTTP 406 and an empty JSON body

**Symptoms:**

* The browser SDK reports HTTP 406 for every envelope it sends.
* The response body is exactly `{}`, with no error detail, for every rejection.
* Collector logs carry repeated lines beginning `SentryReceiver : Error parsing envelop :`.
* Traces from the browser never reach Jaeger, while server-side traces continue to arrive normally.

**Root cause:**

Every parse failure in the envelope parser is funneled into a single 406 response
(`receiver/sentryreceiver/trace-receiver.go:152-157`):

```go
		sr.logger.Sugar().Errorf("SentryReceiver : Error parsing envelop : %+v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		if _, writeErr := w.Write([]byte("{}")); writeErr != nil {
```

Five distinct causes produce that identical response, and only the collector-side log line distinguishes them. From
`receiver/sentryreceiver/envelop_parser.go`:

1. Too few lines in the envelope: `"SentryReceiver : Unexpected number of lines in the envelope : %v. Must be 3 or
   greater"` (line 36).
2. The envelope header is not valid JSON, logged as `"SentryReceiver : Unmarshal header error: %+v"` (line 40).
3. The item type header is not valid JSON, logged as `"SentryReceiver : Unmarshal type_header error: %+v"` (line 55).
4. The payload is not valid JSON, logged as `"SentryReceiver : Unmarshal session event error: %+v ; Payload: %+v"`
   (line 73) or `"SentryReceiver : Unmarshal event error: %+v ; Payload: %+v"` (line 80).
5. Nothing usable was found in the envelope: `"SentryReceiver : No useful payload in the envelop"` (line 89).

A sixth, less obvious cause routes here too. The request body readers at
`receiver/sentryreceiver/trace-receiver.go:205-219` silently return the *raw* reader when decompression setup fails,
and `slurp, _ := io.ReadAll(pr)` at line 142 discards its read error. A corrupt or unexpectedly encoded body therefore
reaches the parser as compressed bytes and surfaces as a generic parse error, with nothing in the logs pointing at
compression.

A large payload lands here as well: fields typed `StrongString` reject data above a hard 50 MB cap during unmarshal
(`receiver/sentryreceiver/models/envelope.go:60-64`), which the parser reports as an ordinary payload unmarshal
failure. See *Pod logs contain full browser payloads* for why that particular log line is itself a problem.

**How to check:**

1. Read the collector-side log line, which is the only thing that distinguishes the five causes.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=200 | grep 'SentryReceiver : '
   ```

   Match the line against the five listed above to identify which parse stage failed.
2. Confirm the SDK is sending to the envelope endpoint rather than to a Sentry-hosted ingest URL, by reading the DSN
   configured in the browser application.
3. Check the failing request's `Content-Encoding` in the browser or ingress capture. The receiver supports `gzip`,
   `deflate`, and `zlib`. A corrupt compressed body can fall back to the raw reader and surface as a generic parse
   failure.

   `No useful payload` alone does not identify a compression problem. It also occurs when all item types are unsupported
   or usable payload lines are empty.

**How to fix:**

1. When the log line names an unmarshal error, correct the producing SDK's payload or compression.
2. When the line reads `No useful payload`, inspect the item types. The receiver accepts only `transaction`, `event`,
   and `session`; an otherwise valid envelope containing only other item types is rejected with 406 after all items are
   skipped.
3. When the log line reads `Unexpected number of lines in the envelope`, the SDK is sending a truncated envelope.
   Envelopes must carry at least a header line, an item header, and an item payload.
4. When the payload is legitimately large, reduce what the SDK attaches. The 50 MB per-field cap is compiled in and
   cannot be configured.

**How to avoid this issue:**

Pin the browser SDK to a version whose envelope format this receiver parses, and avoid attaching large blobs to
messages or exception values. The receiver has no configuration that relaxes any of these limits.

**Data to collect:**

* The `SentryReceiver :` log lines, which name the failing stage.
* The SDK name and version, and the DSN the browser is configured with.
* One failing envelope body, captured from the browser's network panel.

**Sources:**

* Code analysis: `receiver/sentryreceiver/trace-receiver.go:142`, `:152-157`, `:205-219`;
  `receiver/sentryreceiver/envelop_parser.go:36`, `:40`, `:55`, `:73`, `:80`, `:89`;
  `receiver/sentryreceiver/models/envelope.go:60-64`.

### Pod logs contain full browser payloads

**Symptoms:**

* Collector pod logs are unexpectedly large, and log storage or the log shipper is under pressure.
* Log entries contain `SentryReceiver : Start parsing envelop :` followed by a complete envelope body between
  `---START---` and `---END---` markers.
* Entries contain user-supplied content from the browser: URLs, message text, and exception values.

**Root cause:**

The chart ships `LOG_LEVEL: 'debug'` as its default (`charts/open-telemetry-collector/values.yaml:288`), wired into
collector telemetry at `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:232`. At that level
the parser dumps every envelope body it receives (`receiver/sentryreceiver/envelop_parser.go:27`):

```go
	logger.Sugar().Debugf("SentryReceiver : Start parsing envelop :\n---START---\n%+v\n---END---\n", body)
```

Because Sentry envelopes carry whatever the browser application put in them, this writes user-facing data (request
URLs with query parameters, message text, and exception values) into pod logs, which are typically shipped onward to
a log backend.

Note that `docs/installation-notes.md:34` documents the `LOG_LEVEL` default as `info`, while the chart's actual value
is `debug`. The deployed default is the one in `values.yaml`.

A related exposure sits on the error path: the payload unmarshal failures at
`receiver/sentryreceiver/envelop_parser.go:73` and `:80` interpolate the full payload with `%+v` at error level, so
they log the body regardless of `LOG_LEVEL`. For a payload that tripped the 50 MB field cap, that writes the whole
oversized payload into the log.

**How to check:**

1. Read the effective log level from the rendered config rather than from the documentation.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A3 'telemetry:'
   ```

   A healthy production installation shows `level: info` or higher.
2. Confirm the envelope dumps are present.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=200 | grep -c 'Start parsing envelop'
   ```

   Any non-zero count means bodies are being written.

**How to fix:**

1. **DANGEROUS — this change starts a rollout. Until it completes, the old pod can continue writing sensitive envelope
   bodies at debug level. Review *Rollout and restart risk* before applying it.** Raise the log level so the envelope
   dumps stop.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values --set LOG_LEVEL=info
   ```

   Raising the level also silences the debug lines that diagnose missing spans, so lower it again deliberately and
   temporarily when investigating the Sentry path.
2. Purge already-shipped envelope bodies from the log backend according to your data-retention policy. The error-level
   payload dumps described above are not suppressed by this change and will continue for malformed payloads.

**How to avoid this issue:**

Set `LOG_LEVEL` explicitly to `info` in the values you deploy, rather than relying on the chart default. Do not treat
`docs/installation-notes.md` as authoritative for this value.

**Data to collect:**

* The `telemetry` block from the rendered `otel.yaml`.
* The `LOG_LEVEL` value from `helm get values`.
* A count of `Start parsing envelop` lines over a known interval, to size the exposure.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/values.yaml:288`,
  `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:232`,
  `receiver/sentryreceiver/envelop_parser.go:27`, `:73`, `:80`; `docs/installation-notes.md:34`.

## Graylog log export

### No logs reach Graylog and the collector repeats a TCP connection error

**Symptoms:**

* No log messages arrive in Graylog, while traces continue to reach Jaeger normally.
* Collector logs repeat a pair of lines at a fixed interval, by default once a minute:

  ```text
  GraylogTcpConnection : Creating TCP connection #0 to Graylog
  GraylogTcpConnection : Error creating TCP connection #0 to Graylog: <dial error>
  ```

* The collector pod is `Running` and `Ready`, and startup reported no error.
* `otelcol_exporter_send_failed_log_records` stays at zero despite the loss.

**Root cause:**

The Graylog sender dials in an unbounded retry loop and treats a dial failure as non-terminal
(`common/graylog/graylog.go:121-135`):

```go
		gs.logger.Sugar().Infof("GraylogTcpConnection : Creating TCP connection #%d to Graylog", connNumber)
		tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
		if err != nil {
			gs.logger.Sugar().Errorf("GraylogTcpConnection : Error creating TCP connection #%d to Graylog: %+v", connNumber, err)
			time.Sleep(gs.successiveSendErrFreezeTime)
			continue
		}
```

The sleep uses `successive_send_error_freeze_time`, default `"1m"`, which is why the pair repeats once a minute.

The exporter's `start` never dials synchronously; `NewGraylogSender` spawns these goroutines and returns
(`exporter/graylogexporter/graylogexporter.go:94`, `exporter/logtcpexporter/logtcpexporter.go:94`). So a wholly
unreachable Graylog produces a clean collector startup, a `Ready` pod, and passing probes. Nothing in the pod's status
reflects the failure.

Two causes account for almost every occurrence, the first far more often than the second.

1. **`GRAYLOG_COLLECTOR_HOST` was never set.** It defaults to the empty string, and the template interpolates it
   with no fallback and no guard
   (`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:46`):

   ```yaml
         graylogexporter:
           endpoint: {{ .Values.GRAYLOG_COLLECTOR_HOST }}:{{ .Values.GRAYLOG_COLLECTOR_PORT }}
   ```

   With the host empty this renders `endpoint: ":12201"`, which passes the factory's non-empty check because the
   string itself is not empty. Endpoint parsing then yields an empty host and
   `net.JoinHostPort("", "12201")` (`common/graylog/graylog.go:112`) produces `":12201"`, which Go dials as
   localhost. The collector connects to itself, where nothing listens on 12201. Because `OTEC_ENABLE_LOGGING`
   defaults to `true`, this is reachable on any default install that simply omits the Graylog host.

2. **The host is set but unreachable**: the Graylog service is down, its GELF TCP input is not running, or a
   NetworkPolicy blocks egress.

A related trap: a host given without a port silently defaults to 12201
(`exporter/graylogexporter/graylogexporter.go:35`), with no log line recording the substitution. IPv6 literals are
not supported, because the endpoint is split on `:`.

**How to check:**

1. Read the endpoint the exporter was actually configured with.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -B2 -A6 'graylogexporter:'
   ```

   A healthy result shows `endpoint: <host>:12201` with a non-empty host. A bare `endpoint: ":12201"` confirms
   cause 1.
2. Confirm the dial error and read what it says. An address of `127.0.0.1` or a bare `:12201` points at cause 1;
   DNS resolution, connection refused, and timeout against your real host point at cause 2.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=200 | grep 'Error creating TCP connection'
   ```

3. Test reachability from inside the pod, which rules out NetworkPolicy and DNS issues in the collector's own
   namespace.

   ```bash
   kubectl -n <namespace> exec <pod-name> -- nc -zv <graylog-host> 12201
   ```

**How to fix:**

1. For cause 1, supply the Graylog host. Use a bare hostname or IP with no URL scheme, or startup will fail
   instead. See *Collector fails to start with an invalid port in endpoint error*.
   **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set GRAYLOG_COLLECTOR_HOST=<graylog-host>
   ```

   Where logs are not wanted at all, disable the logs path rather than leaving the host empty. Disable
   `OTEC_ENABLE_LOGGING` and `OTEC_ENABLE_LOGDEDUP_PROCESSOR` together, because disabling logging alone breaks
   startup. See *Collector will not start, reporting an exporter that is not configured*.
2. For cause 2, fix the network path rather than the collector: confirm the Graylog Service exists and its GELF TCP
   input is running, and confirm no NetworkPolicy blocks egress from the collector's namespace. The collector
   reconnects on its own once the path opens; it retries forever and needs no restart.

**How to avoid this issue:**

Set `GRAYLOG_COLLECTOR_HOST` explicitly at install time whenever `OTEC_ENABLE_LOGGING` is true. The absence of a dial
error is not proof of delivery, and this exporter has no delivery metric or readiness signal. Confirm the path with a
known test record at the Graylog input or destination.

**Data to collect:**

* The `graylogexporter` and `logtcpexporter` blocks from the rendered `otel.yaml`.
* The `GraylogTcpConnection :` log lines, which carry the dial error verbatim.
* The result of a connectivity test from inside the pod.

**Sources:**

* Code analysis: `common/graylog/graylog.go:112`, `:121-135`;
  `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:46`;
  `exporter/graylogexporter/graylogexporter.go:35`, `:94`, `:97-107`, `factory.go:103`;
  `exporter/logtcpexporter/logtcpexporter.go:94`.

### Collector fails to start with an invalid port in endpoint error

**Symptoms:**

* The pod is in `CrashLoopBackOff` immediately after a `helm upgrade` that set `GRAYLOG_COLLECTOR_HOST`.
* Collector logs contain `invalid port in endpoint:` followed by a parse error.
* The port was not changed, only the host, so the error appears to name the wrong field.
* In some variants the message ends with `%!w(<nil>)` rather than a parse error.

**Root cause:**

Endpoint parsing splits the whole endpoint string on `:` and treats the second field as the port
(`exporter/graylogexporter/graylogexporter.go:97-107`):

```go
func parseEndpoint(endpoint string) (string, uint, error) {
	parts := strings.Split(endpoint, ":")
	if len(parts) == 1 {
		return parts[0], defaultGraylogPort, nil
	}
	port, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in endpoint: %w", err)
	}
```

When `GRAYLOG_COLLECTOR_HOST` carries a URL scheme, the rendered endpoint becomes something like
`https://graylog-host:12201`. Splitting that on `:` gives `["https", "//graylog-host", "12201"]`, so `parts[1]` is
`"//graylog-host"`, which fails to parse as a port. The error names the port even though the real defect is the
scheme.

The `%!w(<nil>)` variant appears when the failure is the range check rather than a parse error, for `"host:0"` or
`"host:70000"`, `err` is `nil` and `%w` has nothing to wrap.

The chart treats the two Graylog-bound exporters differently, which makes the failure look inconsistent. When
rendering the `logtcpexporter` endpoint, the template strips `http://` and `https://` prefixes with `replace` and
falls back from `GRAYLOG_COLLECTOR_HOST` to `GRAYLOG_UI_URL`
(`charts/open-telemetry-collector/templates/metrics_collector_config.yaml:39`, `:158`). The `graylogexporter`
endpoint (line 46) is interpolated verbatim, and neither exporter strips a scheme in its own code. The same value can
therefore work for one exporter and break the other.

`logtcpexporter` emits the same failure with a slightly different string,
`"invalid port in endpoint: %v : %+v"` (`exporter/logtcpexporter/logtcpexporter.go:69`).

**How to check:**

1. Read the rendered endpoint and look for a scheme.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep 'endpoint:'
   ```

   A healthy Graylog endpoint is a bare `host:port` with no `http://` or `https://` prefix.
2. Read the startup error from the previous container, since the current one has already restarted.

   ```bash
   kubectl -n <namespace> logs <pod-name> --previous --tail=50 | grep 'invalid port in endpoint'
   ```

**How to fix:**

1. **DANGEROUS — this change starts a rollout. Review *Rollout and restart risk* before applying it.** Set the host
   without a URL scheme. The Graylog GELF input is a raw TCP socket, not an HTTP endpoint, so a scheme is never correct
   here.

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set GRAYLOG_COLLECTOR_HOST=graylog-logging.<domain>
   ```

2. If the value came from `GRAYLOG_UI_URL`, note that the UI URL legitimately carries a scheme while the collector
   endpoint must not. Set `GRAYLOG_COLLECTOR_HOST` explicitly rather than relying on the fallback, which the chart
   applies only when rendering `logtcpexporter`.

**How to avoid this issue:**

Configure `GRAYLOG_COLLECTOR_HOST` as a bare hostname or IP address. Do not reuse the Graylog web UI URL for it, and
do not rely on `GRAYLOG_UI_URL` as a fallback, since the chart applies that fallback only to `logtcpexporter`.

**Data to collect:**

* The rendered `endpoint` lines for both `graylogexporter` and `logtcpexporter`.
* The `invalid port in endpoint` line from the previous container's logs.
* The `GRAYLOG_COLLECTOR_HOST` and `GRAYLOG_UI_URL` values from `helm get values`.

**Sources:**

* Code analysis: `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:39`, `:46`, `:158`;
  `exporter/graylogexporter/graylogexporter.go:62`, `:97-107`; `exporter/logtcpexporter/logtcpexporter.go:58-73`.

### Log messages are dropped with a message queue is full warning

**Symptoms:**

* Logs arrive in Graylog with gaps, or stop for roughly a minute at a time and then resume.
* Collector logs contain `Failed to enqueue message` with `message queue is full`, or
  `has not been put to the graylog queue`.
* Shortly before the gap, the logs contain
  `GraylogTcpConnection : <N> successive errors in goroutine #<N>, freezing for 1m0s`.
* `otelcol_exporter_send_failed_log_records` remains at zero throughout.

**Root cause:**

Two mechanisms combine, and the coupling between them is what produces the periodic gaps.

First, the queue drops on overflow rather than applying back pressure
(`common/graylog/graylog.go:202-211`):

```go
func (gs *GraylogSender) SendToQueue(m *Message) error {
	select {
	case gs.msgQueue <- m:
		return nil
	case <-gs.ctx.Done():
		return fmt.Errorf("sender stopped")
	default:
		return fmt.Errorf("message queue is full")
	}
}
```

The `default` arm makes the send non-blocking, so a full queue discards the message immediately.

Second, the sender freezes on repeated send errors (`common/graylog/graylog.go:186-190`):

<!-- markdownlint-disable line-length -->
```go
				if successiveGraylogErrCnt > gs.maxSuccessiveSendErrCnt {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : %d successive errors in goroutine #%d, freezing for %s", successiveGraylogErrCnt, connNumber, gs.successiveSendErrFreezeTime)
					time.Sleep(gs.successiveSendErrFreezeTime)
					successiveGraylogErrCnt = 0
				}
```
<!-- markdownlint-enable line-length -->

With the default connection pool size of one, nothing drains `msgQueue` during that sleep. The condition is strictly
greater than `max_successive_send_error_count`, so the default value of 5 freezes the sender after the sixth
consecutive send error. The queue then fills, and new messages are discarded until space becomes available. The freeze
is therefore usually the cause of the drops rather than a separate problem.

A panic in a sender goroutine triggers the same freeze through a different path
(`common/graylog/graylog.go:103-110`), logging
`GraylogTcpConnection : Panic in goroutine #%d : %+v ; Stacktrace : %s` before sleeping and restarting the goroutine.

**How to check:**

1. Count the drops and locate them in time.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=2000 --timestamps \
     | grep -E 'message queue is full|has not been put to the graylog queue'
   ```

2. Look for a freeze immediately before each cluster of drops. That ordering confirms the coupling.

   ```bash
   kubectl -n <namespace> logs <pod-name> --tail=2000 --timestamps | grep 'freezing for'
   ```

3. Read the configured queue size and freeze settings.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A8 'graylogexporter:'
   ```

**How to fix:**

Fix the send failures first. Enlarging the queue only extends how long the collector can absorb a freeze; it does not
stop the freeze.

1. Resolve why sends are failing, using *No logs reach Graylog and the collector repeats a TCP connection error*. A
   healthy Graylog connection removes the freeze and therefore the drops.
2. Where sends fail only in bursts and the connection is otherwise healthy, raise throughput and shorten the freeze so
   a transient failure costs less. **DANGEROUS — this starts a rollout, raises concurrent Graylog connections, and
   increases memory use. Review backend limits and *Rollout and restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> --reuse-values \
     --set OTEC_GRAYLOGEXPORTER_PARAMETERS.queue_size=10000 \
     --set OTEC_GRAYLOGEXPORTER_PARAMETERS.connection_pool_size=4 \
     --set OTEC_GRAYLOGEXPORTER_PARAMETERS.successive_send_error_freeze_time=5s
   ```

   Note the underscore spelling: `logtcpexporter` uses hyphens for the same settings, and mixing them fails at
   startup. See *Collector will not start in Sentry mode because Graylog keys use the wrong convention*.

**How to avoid this issue:**

Alert on the drop lines themselves. Neither Graylog-bound exporter is instrumented at all, and both return success
to the pipeline regardless of outcome, so `otelcol_exporter_send_failed_log_records` stays at zero while records are
being discarded. A healthy exporter dashboard is not evidence that logs reached Graylog. The log text is the only
signal this path produces.

Beyond queue overflow, four further drop paths exist, all log-only: retry exhaustion
(`GraylogTcpConnection : Message %+v skipped after %d retries in goroutine #%d`, `common/graylog/graylog.go:147`),
a nil message (`:165`), a message that could not be prepared (`:171`), and a log record that could not be converted
(`exporter/graylogexporter/graylogexporter.go:119`). Alert on all of them, not only on the queue.

**Data to collect:**

* Timestamped `message queue is full` and `freezing for` lines covering one full gap.
* The `graylogexporter` and `logtcpexporter` configuration blocks.
* The Graylog server's own logs for the same window.

**Sources:**

* Code analysis: `common/graylog/graylog.go:103-110`, `:186-190`, `:202-211`;
  `exporter/graylogexporter/graylogexporter.go:123`; `exporter/logtcpexporter/logtcpexporter.go:167`, `:226`, `:561`.

### Collector will not start in Sentry mode because Graylog keys use the wrong convention

**Symptoms:**

* Helm renders successfully after Sentry mode is selected and `GRAYLOG_COLLECTOR_HOST` is supplied, but the collector
  pod enters `CrashLoopBackOff`.
* Collector logs report an unmarshaling error naming a key that looks correct.
* The error names one or more hyphenated keys under `graylogexporter`.

**Root cause:**

The Sentry-mode template inserts `OTEC_LOGTCPEXPORTER_PARAMETERS` into both `logtcpexporter` and `graylogexporter`.
The two exporters write to the same Graylog destination but declare the same settings with different naming
conventions. `exporter/logtcpexporter/config.go:25-33` uses hyphens:

```text
arbitrary-traces-logging
connection-pool-size
queue-size
max-message-send-retry-count
max-successive-send-error-count
successive-send-error-freeze-time
```

`exporter/graylogexporter/config.go:33-41` uses underscores:

```text
field_mapping
connection_pool_size
queue_size
max_message_send_retry_count
max_successive_send_error_count
successive_send_error_freeze_time
```

The collector core unmarshals component configuration strictly, so a key in the wrong convention is rejected rather
than ignored. The chart's default hyphenated `OTEC_LOGTCPEXPORTER_PARAMETERS` therefore makes the generated
`graylogexporter` block invalid in Sentry mode.

The GELF mapping under `graylogexporter` mixes both conventions in one block: `field_mapping` is underscored while its
children `short-message` and `full-message` are hyphenated (`exporter/graylogexporter/config.go:25-31`).

The same mismatch can also be introduced manually in span-metrics mode by copying a settings block between the two
exporters. In addition, `docs/user-guide.md` documents a `Batch-size` setting that neither exporter accepts.

**How to check:**

1. Read the startup error from the previous container.

   ```bash
   kubectl -n <namespace> logs <pod-name> --previous --tail=50
   ```

2. Read the rendered blocks and check each key's convention against the lists above.

   ```bash
   kubectl -n <namespace> get configmap opentelemetry-collector-config -o jsonpath='{.data.otel\.yaml}' \
     | grep -A8 -E 'graylogexporter:|logtcpexporter:'
   ```

   `graylogexporter` keys must be underscored; `logtcpexporter` keys must be hyphenated.

**How to fix:**

1. Do not try to choose one spelling for `OTEC_LOGTCPEXPORTER_PARAMETERS` in Sentry mode. The same value is inserted
   into two incompatible component schemas, so one of them remains wrong.
2. Fix the chart to use `OTEC_GRAYLOGEXPORTER_PARAMETERS` for `graylogexporter`, or supply a reviewed full `CONFIG_MAP`
   with each block using its own convention.
3. **DANGEROUS — this correction replaces collector configuration and starts a rollout. A remaining wrong key keeps
   the replacement pod in `CrashLoopBackOff`. Render and validate the full configuration, then review *Rollout and
   restart risk* before applying it.**

   ```bash
   helm -n <namespace> upgrade <release-name> <chart-path> -f <corrected-values-file>
   ```

**How to avoid this issue:**

Render Sentry mode in CI and validate the resulting collector configuration. Outside the defective Sentry template,
keep `OTEC_GRAYLOGEXPORTER_PARAMETERS` underscored and `OTEC_LOGTCPEXPORTER_PARAMETERS` hyphenated.

**Data to collect:**

* The previous container's startup error in full.
* Both exporter blocks from the rendered `otel.yaml`.
* The values you supplied, from `helm get values`.

**Sources:**

* Code analysis: `exporter/logtcpexporter/config.go:25-33`, `:42`; `exporter/graylogexporter/config.go:25-31`,
  `:33-41`, `:60`; `charts/open-telemetry-collector/templates/metrics_collector_config.yaml:162`;
  `docs/user-guide.md:182`.
