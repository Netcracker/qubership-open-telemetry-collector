# Note: this uses host platform for the build, and we ask go build to target the needed platform, so we do not spend time on qemu emulation when running "go build"
FROM --platform=$BUILDPLATFORM golang:1.23.4-alpine3.21 AS builder
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=""
ENV GOSUMDB=off \
    GO111MODULE=on

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

COPY *.go ./
COPY connector/ connector/
COPY exporter/ exporter/
COPY receiver/ receiver/
COPY utils/ utils/

RUN go mod download -x

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o qubership-open-telemetry-collector .

FROM alpine:3.21.0
COPY ./sample.log /sample.log
RUN chmod 644 /sample.log
COPY --from=builder --chown=10001:0 /workspace/qubership-open-telemetry-collector /otec
