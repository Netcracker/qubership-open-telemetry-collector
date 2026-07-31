# Repository agent instructions

## Scope

- This repository maintains the Qubership OpenTelemetry Collector distribution, its Helm chart, and a read-only
  troubleshooting APM package.
- This file contains repository-wide guidance. Put component-specific instructions next to the affected component.

## Repository map

- `builder-config.yaml` defines the assembled collector. Custom Go modules live under `receiver/`, `connector/`,
  `exporter/`, `common/`, and `utils/`.
- `collector/` contains the generated distribution entry points and module files.
- `charts/open-telemetry-collector/` contains the Helm chart and collector configuration template.
- `agent-packages/troubleshoot-otec/` contains the troubleshooting skill. `docs/troubleshooting.md` is a symlink to
  its reference catalog.

## Commands

- Run commands from the repository root unless a command changes directories explicitly.
- Focused Go tests: run `(cd <module-directory> && go test ./...)` for the affected module.
- Full Go verification from the repository root:

  ```bash
  for dir in collector common/graylog connector/sentrymetricsconnector exporter/graylogexporter \
    exporter/logtcpexporter receiver/sentryreceiver utils; do
    (cd "$dir" && go test ./...) || exit
  done
  ```

- Regenerate the collector: `make install-builder build-collector`.
- Lint the Helm chart: `helm lint charts/open-telemetry-collector`.
- Run a specific pre-commit hook on changed files with `pre-commit run <hook-id> --files <paths>`. Do not run the
  `pre-commit-update` hook unless the task is to update hook revisions.
- Check the troubleshooting catalog parser:
  `python3 agent-packages/troubleshoot-otec/.apm/skills/troubleshoot-otec/scripts/show_cases.py
  agent-packages/troubleshoot-otec/.apm/skills/troubleshoot-otec/references/troubleshooting.md`.

## Non-obvious invariants

- Files in `collector/` that carry the builder-generated header, including `go.mod`, `components.go`, and `main*.go`,
  are outputs. Change `builder-config.yaml` or the custom component module, then regenerate the collector.
- Add or remove collector components in `builder-config.yaml`; do not hand-edit registration in
  `collector/components.go`.
- The repository has separate Go modules instead of a root module. Run Go commands from the module that owns the
  change, and use the full module loop for cross-module changes.
- Edit the troubleshooting source under `agent-packages/troubleshoot-otec/.apm/skills/troubleshoot-otec/` rather than
  replacing the `docs/troubleshooting.md` symlink.

## Done when

- Tests for each affected Go module pass. Cross-module or collector assembly changes pass the full Go module loop.
- Collector configuration changes are regenerated, and the generated diff matches `builder-config.yaml`.
- Chart changes pass `helm lint charts/open-telemetry-collector`.
- Changed files pass the applicable pre-commit hooks and PR linters. Troubleshooting catalog changes pass the parser
  command above.
- The final response lists checks run and checks that could not be run.

## Context routing

- Before changing Sentry ingestion, metrics, or log mapping, read `docs/sentry-receiver.md` for the data contracts.
- Before changing Graylog export behavior, read `docs/graylog-exporter.md` for its supported configuration.
- Before changing Helm deployment behavior, read `docs/installation-notes.md` and `docs/user-guide.md` for supported
  values and deployment assumptions.
- Before changing the troubleshooting package, read `agent-packages/troubleshoot-otec/README.md` and its `SKILL.md`;
  they define the package layout, evidence rules, and read-only scope.
