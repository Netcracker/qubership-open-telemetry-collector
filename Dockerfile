# hadolint global ignore=DL3018
# Stage 1: Build
# Base image: golang:1.26.4-alpine3.24
FROM --platform=$BUILDPLATFORM golang@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder

ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

ENV GO111MODULE=on

WORKDIR /build

# Copy the manifest file and other necessary files
COPY builder-config.yaml builder-config.yaml
COPY ./collector ./collector
COPY ./connector ./connector
COPY ./exporter ./exporter
COPY ./receiver ./receiver
COPY ./common ./common
COPY ./utils ./utils
COPY ./tools ./tools

WORKDIR /build/tools

# Install the builder tool and dependencies
RUN apk add --no-cache git \
    && go install -mod=readonly "go.opentelemetry.io/collector/cmd/builder@$(go list -m -f '{{.Version}}' go.opentelemetry.io/collector/cmd/builder)"

WORKDIR /build
RUN CGO_ENABLED=0 builder --config=builder-config.yaml

# Base image: alpine:3.24
FROM alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

ENV USER_ID=65534

WORKDIR /app

# Copy the generated collector binary from the builder stage
COPY --from=builder --chown=${USER_ID} /build/collector/qubership-otec /app/qubership-otec

#Copy the configuration file
#COPY config.yaml ./conf/otel.yaml

# Expose necessary ports
EXPOSE 4317/tcp 4318/tcp 13133/tcp

USER ${USER_ID}

# Set the default entrypoint and command
ENTRYPOINT ["/app/qubership-otec"]
CMD ["--config=otel.yaml"]
