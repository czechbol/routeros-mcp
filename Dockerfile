# syntax=docker/dockerfile:1.7
# Multi-arch build. Use:
#   docker buildx build --platform linux/arm64,linux/arm/v7,linux/amd64 -t routeros-mcp .
# For RouterOS container deploy, see `mage tarballs` / `mage release`.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags='-s -w' -o /out/routeros-mcp .

FROM scratch
COPY --from=build /out/routeros-mcp /routeros-mcp
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
EXPOSE 8080
ENTRYPOINT ["/routeros-mcp"]
