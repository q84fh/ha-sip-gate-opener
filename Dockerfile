FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN go test ./... && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} go build -trimpath -ldflags="-s -w" -o /gate-opener .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gate-opener /gate-opener
# numeric UID, so Kubernetes runAsNonRoot can verify it
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/gate-opener"]
CMD ["serve"]
