# Fetch the latest release tag from opentelemetry-collector-contrib (strips the leading 'v')
OTEL_VERSION ?= $(shell curl -fsSL https://api.github.com/repos/open-telemetry/opentelemetry-collector-contrib/releases/latest \
                  | grep '"tag_name"' | grep -oP 'v\K[0-9]+\.[0-9]+\.[0-9]+')

# Derive the stable API version from the core version (stable = 1.(core_minor - 94).patch)
OTEL_CORE_MINOR := $(word 2, $(subst ., ,$(OTEL_VERSION)))
OTEL_STABLE_VERSION := 1.$(shell expr $(OTEL_CORE_MINOR) - 94).0

# Current versions in the codebase (auto-detected from Dockerfile)
OTEL_CURRENT_VERSION := $(shell grep -oP 'OTEL_VERSION=\K[0-9]+\.[0-9]+\.[0-9]+' Dockerfile)
OTEL_CURRENT_STABLE_VERSION := $(shell grep -oP 'confmap/provider/envprovider v\K[0-9]+\.[0-9]+\.[0-9]+' builder-config.yaml)

.PHONY: update-otel update-versions install-builder build-collector help

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

update-otel: update-versions install-builder build-collector ## Fetch latest OTEL release, update files, and rebuild collector

LOCAL_GOMODS := $(shell find connector exporter receiver common -name 'go.mod' 2>/dev/null)

update-versions: ## Update version references in Dockerfile, builder-config.yaml, and local go.mod files to latest release
	@if [ -z "$(OTEL_VERSION)" ]; then echo "ERROR: could not determine latest OTEL version" >&2; exit 1; fi
	@echo "Updating OTEL version: $(OTEL_CURRENT_VERSION) -> $(OTEL_VERSION)"
	@echo "Updating stable API version: $(OTEL_CURRENT_STABLE_VERSION) -> $(OTEL_STABLE_VERSION)"
	@sed -i 's/OTEL_VERSION=$(OTEL_CURRENT_VERSION)/OTEL_VERSION=$(OTEL_VERSION)/' Dockerfile
	@sed -i 's/v$(OTEL_CURRENT_VERSION)/v$(OTEL_VERSION)/g' builder-config.yaml
	@sed -i 's/v$(OTEL_CURRENT_STABLE_VERSION)/v$(OTEL_STABLE_VERSION)/g' builder-config.yaml
	@for f in $(LOCAL_GOMODS); do \
	  sed -i 's|v$(OTEL_CURRENT_VERSION)|v$(OTEL_VERSION)|g' "$$f"; \
	  sed -i 's|v$(OTEL_CURRENT_STABLE_VERSION)|v$(OTEL_STABLE_VERSION)|g' "$$f"; \
	done
	@echo "Done. Updated Dockerfile, builder-config.yaml, and $(words $(LOCAL_GOMODS)) local go.mod files."

install-builder: ## Install ocb (otelcol-builder) at the target OTEL_VERSION
	@echo "Installing go.opentelemetry.io/collector/cmd/builder@v$(OTEL_VERSION)"
	go install go.opentelemetry.io/collector/cmd/builder@v$(OTEL_VERSION)

build-collector: ## Run builder to regenerate collector/go.mod and collector source
	@echo "Running builder --config=builder-config.yaml"
	builder --config=builder-config.yaml
