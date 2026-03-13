# Fetch the latest release tag from opentelemetry-collector-contrib (strips the leading 'v')
OTEL_VERSION ?= $(shell curl -fsSL https://api.github.com/repos/open-telemetry/opentelemetry-collector-contrib/releases/latest \
                  | grep '"tag_name"' | grep -oP 'v\K[0-9]+\.[0-9]+\.[0-9]+')

# Derive the stable API version from the core version (stable = 1.(core_minor - 94).patch)
OTEL_CORE_MINOR := $(word 2, $(subst ., ,$(OTEL_VERSION)))
OTEL_STABLE_VERSION := 1.$(shell expr $(OTEL_CORE_MINOR) - 94).0

# Current versions in the codebase (auto-detected from Dockerfile)
OTEL_CURRENT_VERSION := $(shell grep -oP 'OTEL_VERSION=\K[0-9]+\.[0-9]+\.[0-9]+' Dockerfile)
OTEL_CURRENT_STABLE_VERSION := $(shell grep -oP 'confmap/provider/envprovider v\K[0-9]+\.[0-9]+\.[0-9]+' builder-config.yaml)

# Semconv spec version is embedded in Go import paths (e.g. go.opentelemetry.io/otel/semconv/v1.40.0)
SEMCONV_CURRENT_VERSION := $(shell find connector exporter receiver common -name '*.go' 2>/dev/null \
                              | xargs grep -h 'go.opentelemetry.io/otel/semconv' 2>/dev/null \
                              | grep -oP 'semconv/v\K[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -1)
# go.opentelemetry.io/otel SDK version (distinct from the collector stable version)
# - detected from direct (non-indirect) dependencies across all local go.mod files
OTEL_SDK_VERSION := $(shell find connector exporter receiver common -name 'go.mod' 2>/dev/null \
                      | xargs grep -h '^	go.opentelemetry.io/otel v' 2>/dev/null \
                      | grep -oP '\botel v\K[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -1)
# Fetch the latest semconv spec version available in the target OTel SDK from the opentelemetry-go repo
# Use ifndef + := so the curl runs exactly once at parse time (not once per reference)
ifndef SEMCONV_NEW_VERSION
SEMCONV_NEW_VERSION := $(shell curl -fsSL \
                          'https://api.github.com/repos/open-telemetry/opentelemetry-go/contents/semconv?ref=v$(OTEL_SDK_VERSION)' \
                          | grep -oP '"name":\s*"v\K[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -1)
endif

.PHONY: update-otel update-versions update-semconv install-builder build-collector help

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

update-otel: update-versions update-semconv install-builder build-collector ## Fetch latest OTEL release, update files, and rebuild collector

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

update-semconv: ## Update go.opentelemetry.io/otel/semconv import version in all Go source files
	@if [ -z "$(SEMCONV_CURRENT_VERSION)" ]; then echo "ERROR: could not detect current semconv version in Go source files" >&2; exit 1; fi
	@if [ -z "$(SEMCONV_NEW_VERSION)" ]; then echo "ERROR: could not determine new semconv version for OTel SDK v$(OTEL_SDK_VERSION)" >&2; exit 1; fi
	@if [ "$(SEMCONV_CURRENT_VERSION)" = "$(SEMCONV_NEW_VERSION)" ]; then \
	  echo "semconv already at v$(SEMCONV_NEW_VERSION), nothing to do"; \
	else \
	  echo "Updating semconv: v$(SEMCONV_CURRENT_VERSION) -> v$(SEMCONV_NEW_VERSION)"; \
	  find connector exporter receiver common -name '*.go' 2>/dev/null \
	    -exec sed -i 's|go\.opentelemetry\.io/otel/semconv/v$(SEMCONV_CURRENT_VERSION)|go.opentelemetry.io/otel/semconv/v$(SEMCONV_NEW_VERSION)|g' {} +; \
	  echo "Done. Updated semconv imports in Go source files."; \
	fi

install-builder: ## Install ocb (otelcol-builder) at the target OTEL_VERSION
	@echo "Installing go.opentelemetry.io/collector/cmd/builder@v$(OTEL_VERSION)"
	go install go.opentelemetry.io/collector/cmd/builder@v$(OTEL_VERSION)

build-collector: ## Run builder to regenerate collector/go.mod and collector source
	@echo "Running builder --config=builder-config.yaml"
	builder --config=builder-config.yaml
