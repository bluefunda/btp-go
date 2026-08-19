# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first: this layer is cached until go.mod or go.sum changes,
# which is almost never compared to the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off and a stripped binary so the result runs on a distroless base with
# no libc at all.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/gateway ./cmd/gateway

# ---- runtime --------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=build /out/gateway /gateway

# nonroot (uid 65532) is baked into the base image. The gateway holds no
# state on disk, so nothing needs a writable volume.
USER nonroot:nonroot

EXPOSE 8080

# The container has no shell, so the probes have to be configured in the
# orchestrator rather than as a HEALTHCHECK:
#   livenessProbe:  GET /healthz
#   readinessProbe: GET /readyz
ENTRYPOINT ["/gateway"]
