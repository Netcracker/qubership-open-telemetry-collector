#!/usr/bin/env bash

set -euo pipefail

chart_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
rendered_manifest=$(mktemp)
trap 'rm -f "${rendered_manifest}"' EXIT

assert_contains() {
    local pattern=$1
    local message=$2

    if ! grep -Eq "${pattern}" "${rendered_manifest}"; then
        echo "Hardening check failed: ${message}" >&2
        exit 1
    fi
}

assert_absent() {
    local pattern=$1
    local message=$2

    if grep -Eq "${pattern}" "${rendered_manifest}"; then
        echo "Hardening check failed: ${message}" >&2
        exit 1
    fi
}

render_and_check() {
    local platform=$1

    helm template security-hardening "${chart_dir}" \
        --set "PAAS_PLATFORM=${platform}" \
        --set MONITORING_ENABLED=true \
        --set OTEC_SENTRY_ENVELOPES_INGRESS_ENABLED=true \
        >"${rendered_manifest}"

    assert_contains 'runAsNonRoot: true' 'pod security context must require a non-root user'
    assert_contains 'type: RuntimeDefault' 'pod security context must use the default seccomp profile'
    assert_contains 'readOnlyRootFilesystem: true' 'container root filesystem must be read-only'
    assert_contains 'allowPrivilegeEscalation: false' 'privilege escalation must be disabled'
    assert_contains 'drop:' 'container capabilities must be dropped'
    assert_contains '^[[:space:]]*- ALL$' 'all container capabilities must be dropped'
    assert_contains 'name: tmp' 'the pod must define and mount temporary storage'
    assert_contains 'sizeLimit: 100Mi' 'temporary storage must have a size limit'
    assert_contains 'ephemeral-storage: 100Mi' 'the container must request ephemeral storage'
    assert_contains 'ephemeral-storage: 200Mi' 'the container must limit ephemeral storage'
    assert_absent 'hostNetwork: true|hostPID: true|hostIPC: true|hostPath:' 'host namespaces and paths are forbidden'

    if [[ ${platform} == KUBERNETES ]]; then
        assert_contains 'runAsUser: 10001' 'Kubernetes pods must use the configured non-root UID'
        assert_contains 'runAsGroup: 10001' 'Kubernetes pods must use the configured non-root GID'
    else
        assert_absent 'runAsUser:|runAsGroup:' 'OpenShift must assign the runtime UID and GID'
        assert_absent 'kind: DeploymentConfig' 'the HPA must target the Deployment rendered by the chart'
    fi
}

render_and_check KUBERNETES
render_and_check OPENSHIFT

echo "Security hardening checks passed."
