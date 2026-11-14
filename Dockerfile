# Base images are pinned by digest; Dependabot keeps tags and digests up to date.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH TARGETVARIANT VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN go test ./... && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /gate-opener .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /gate-opener /gate-opener
# numeric UID, so Kubernetes runAsNonRoot can verify it
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/gate-opener"]
CMD ["serve"]
