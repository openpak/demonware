# Multi-stage, cross-compiled: the build stage runs on the runner's native arch and Go
# emits the target arch, so linux/arm64 builds need no QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
# No dependencies outside the standard library.
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/demonwared

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 65532 -h /app app
WORKDIR /app
COPY --from=build /out/app /app/app
RUN mkdir -p /app/data && chown -R 65532:65532 /app
ENV DEMONWARE_DATA=/app/data
# Unprivileged: every port it binds (3074, 22610-22619) is above 1024.
USER 65532:65532
ENTRYPOINT ["/app/app"]
