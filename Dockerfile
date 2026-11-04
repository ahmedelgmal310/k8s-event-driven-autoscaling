FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# One image, two entrypoints: the chart runs /api or /worker.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /out/worker ./cmd/worker

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/ahmedelgmal310/k8s-event-driven-autoscaling" \
      org.opencontainers.image.title="event-autoscaling" \
      org.opencontainers.image.description="Go API and queue worker autoscaled by KEDA on CPU, request rate and queue lag" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /out/api /api
COPY --from=build /out/worker /worker
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/api"]
