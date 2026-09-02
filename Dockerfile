# syntax=docker/dockerfile:1.7

FROM golang:1.26.8-alpine3.23 AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates

ARG GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    GOPROXY="${GOPROXY}" go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOPROXY="${GOPROXY}" go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Version=${VERSION} -X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Commit=${COMMIT} -X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Date=${BUILD_DATE}" \
      -o /out/cairn-x-enricher \
      ./cmd/cairn-x-enricher

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/cairn-x-enricher /cairn-x-enricher

ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/cairn-x-enricher", "healthcheck"]

ENTRYPOINT ["/cairn-x-enricher"]
CMD ["serve"]
