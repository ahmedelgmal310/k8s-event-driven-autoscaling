FROM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build

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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

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
