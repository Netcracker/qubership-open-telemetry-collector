#!/usr/bin/env bash

set -euo pipefail

chart_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
rendered=$(mktemp)
trap 'rm -f "${rendered}"' EXIT

for mode in SPAN_METRICS_PROCESSOR SENTRY_ENVELOPES_PROCESSING; do
    for format in json text console; do
        expected=json
        if [[ ${format} != json ]]; then
            expected=console
        fi
        helm template logging-test "${chart_dir}" \
            --show-only templates/metrics_collector_config.yaml \
            --set "OTEC_INSTALLATION_MODE=${mode}" --set "OTEC_LOG_FORMAT=${format}" \
            --set GRAYLOG_COLLECTOR_HOST=127.0.0.1 --set GRAYLOG_COLLECTOR_PORT=12201 \
            --set OTEC_GRAYLOGEXPORTER_PARAMETERS.connection_pool_size=7 >"${rendered}"
        if ! grep -Eq "encoding: \"${expected}\"" "${rendered}"; then
            echo "Unexpected logging encoding for ${mode}/${format}" >&2
            cat "${rendered}" >&2
            exit 1
        fi
        if [[ ${mode} == SENTRY_ENVELOPES_PROCESSING ]] && ! grep -q 'connection_pool_size: 7' "${rendered}"; then
            echo "Sentry Graylog exporter must receive OTEC_GRAYLOGEXPORTER_PARAMETERS" >&2
            exit 1
        fi
    done
done

echo "Logging configuration checks passed."
