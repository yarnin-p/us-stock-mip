# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mip ./cmd/mip

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mip /usr/local/bin/mip
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/mip"]
