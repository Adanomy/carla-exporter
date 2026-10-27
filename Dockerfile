ARG SOURCE_DATE_EPOCH=0
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
ENV GOTOOLCHAIN=local GOWORK=off GOENV=off
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
RUN test "$(go env GOVERSION)" = "go$(awk '/^go / {print $2}' go.mod)" && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOAMD64=v1 GOARM64=v8.0 GOFLAGS= GOEXPERIMENT= \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -buildid= -X main.version=$VERSION -X main.revision=$REVISION" \
    -o /out/carla-exporter ./cmd/carla-exporter

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/Adanomy/carla-exporter" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.title="CARLA Exporter"
COPY --from=build /out/carla-exporter /carla-exporter
USER 65532:65532
EXPOSE 9840
ENTRYPOINT ["/carla-exporter"]
