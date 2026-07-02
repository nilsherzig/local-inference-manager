# Build lim on top of the upstream llama.cpp server image, which already ships
# /app/llama-server. lim replaces the image's entrypoint: it manages that binary
# on demand and proxies OpenAI-compatible requests to it.
#
# Base image is selectable: defaults to the Vulkan server image; override with
# LLAMA_IMAGE=...:server-cuda to build the CUDA variant. A global ARG (declared
# before the first FROM) is the only kind usable in a later FROM.
ARG LLAMA_IMAGE=ghcr.io/ggml-org/llama.cpp:server-vulkan

FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG BUILD_TIME
ARG GIT_COMMIT
# Pure-Go sqlite (glebarez/modernc) -> no cgo. The Tailwind stylesheet is
# vendored (internal/web/static/app.css) and embedded via go:embed, so the
# build needs neither node nor tailwindcss.
RUN CGO_ENABLED=0 go build -o /lim ./cmd/lim

FROM ${LLAMA_IMAGE}

COPY --from=builder /lim /usr/local/bin/lim

# The upstream image ships llama-server's shared libs in /app and relies on its
# WORKDIR=/app entrypoint to find them. lim starts /app/llama-server as a
# subprocess from a different CWD, so the dynamic linker needs /app spelled out.
ENV LD_LIBRARY_PATH=/app

WORKDIR /data

EXPOSE 8080

# Config comes from a mounted ConfigMap; the SQLite DB (tokens, request log)
# lives in the mounted /data volume so it survives restarts.
ENTRYPOINT ["lim", "-config", "/config/config.yaml", "--show-llama-logs"]
