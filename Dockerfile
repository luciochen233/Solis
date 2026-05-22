# ==========================================
# Build Stage
# ==========================================
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

WORKDIR /build

# Install git and ca-certificates (needed for downloading modules and SSL)
RUN apk add --no-cache git ca-certificates

# Copy go.mod and go.sum and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Compile static binary optimized for Raspberry Pi Zero (ARMv6, no CGO)
# - ldflags "-s -w" removes symbol tables and debugging info, reducing binary size
ARG TARGETOS=linux
ARG TARGETARCH=arm
ARG TARGETVARIANT=v6

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=6 \
    go build -ldflags="-s -w" -o solis main.go

# ==========================================
# Final Stage
# ==========================================
FROM alpine:3.20

# Install basic certificates and tools
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Create a non-root user for security
RUN addgroup -S solis && adduser -S solis -G solis

# Create data directory and change ownership
RUN mkdir -p /app/data && chown -R solis:solis /app/data

# Copy the compiled binary from the builder
COPY --from=builder /build/solis /app/solis

# Copy example config (and default config)
COPY config.toml.example /app/config.toml.example
COPY config.toml.example /app/config.toml

# Set permissions for the non-root user
RUN chown -R solis:solis /app

# Switch to the non-root user
USER solis

# Persist DB and uploads in a mounted volume
VOLUME ["/app/data"]

# Expose the default premium port
EXPOSE 8889

# Run the lightweight binary
ENTRYPOINT ["/app/solis", "--config", "/app/config.toml"]
