# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mip ./cmd/mip

FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MIP_ML_PYTHON=/usr/local/bin/python3
WORKDIR /app
RUN apt-get update && \
    apt-get install --no-install-recommends -y libgomp1 && \
    rm -rf /var/lib/apt/lists/*
COPY requirements-ml-runtime.txt ./
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --disable-pip-version-check -r requirements-ml-runtime.txt
COPY scripts/ml_bridge.py ./scripts/ml_bridge.py
COPY --from=build /out/mip /usr/local/bin/mip
RUN useradd --system --uid 65532 --create-home mip
USER mip
ENTRYPOINT ["/usr/local/bin/mip"]
