# Repository agent instructions

## Generated collector changes

- Use the `Makefile` targets for collector generation instead of invoking the underlying builder directly.
- Regenerate the assembled collector after changing `builder-config.yaml`, the builder module, component module
  dependencies, or any dependency version that can affect the collector module. Do not assume that a dependency-only
  update leaves generated files unchanged.
- Run `make install-builder build-collector` from the repository root, then run `(cd collector && go mod tidy)`.
- Do not edit files under `collector/` that carry a builder-generated header. Treat `collector/go.mod` and
  `collector/go.sum` as generated module metadata, and commit changes produced by the builder and `go mod tidy`.
- Before finishing, rerun the applicable Make targets and confirm that generation produces no additional changes.
  Run `(cd collector && go test -mod=readonly ./...)` to catch stale module metadata.
