# hadolint global ignore=DL3018
# Stage 1: Build
FROM --platform=$BUILDPLATFORM golang:1.24.3-alpine3.21 AS builder

ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

ARG OTEL_VERSION=0.129.0

ENV GOSUMDB=off \
    GO111MODULE=on

WORKDIR /build

# Copy the manifest file and other necessary files
COPY builder-config.yaml builder-config.yaml
COPY ./collector ./collector
COPY ./connector ./connector
COPY ./exporter ./exporter
COPY ./receiver ./receiver
COPY ./common ./common
COPY ./utils ./utils

# Install the builder tool and dependencies
RUN apk add --no-cache git \
    && go install go.opentelemetry.io/collector/cmd/builder@v${OTEL_VERSION} \
    # Build the collector
    && CGO_ENABLED=0 builder --config=builder-config.yaml

FROM alpine:3.22

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
