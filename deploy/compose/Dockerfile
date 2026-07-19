# Shared multi-stage build for all bank-core services.
# Build arg SERVICE selects the service; the whole workspace is the build context
# so cross-module (pkg/, gen/go) changes are always picked up.
FROM golang:1.26 AS build
ARG SERVICE
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/app ./services/${SERVICE}/cmd
# Writable mount point for volumes (identity keys): a named volume created
# over this path inherits nonroot ownership from the image.
RUN mkdir -p /out/keys

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=nonroot:nonroot /out/keys /keys
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
