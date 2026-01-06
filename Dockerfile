# RedstoneCore Agent Dockerfile
# Multi-stage build: Go builder + Java runtime

# Stage 1: Build the Go binary
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy source code
COPY . .

# Download dependencies and tidy
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s -X main.version=1.0.0 -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /rsc \
    ./cmd/rsc

# Stage 2: Runtime with Java
FROM eclipse-temurin:21-jre-alpine

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    curl \
    bash \
    tzdata \
    docker-cli \
    docker-cli-compose

# Create non-root user for security
RUN addgroup -S rsc && adduser -S rsc -G rsc

# Create directories
RUN mkdir -p /data /config && chown -R rsc:rsc /data /config

# Copy the binary from builder
COPY --from=builder /rsc /usr/local/bin/rsc
RUN chmod +x /usr/local/bin/rsc

# Set working directory
WORKDIR /data

# Environment variables
ENV RSC_DATA_DIR=/data \
    RSC_CONFIG_DIR=/config \
    TZ=UTC

# Volumes for persistence
VOLUME ["/data", "/config"]

# Expose Minecraft ports (default range)
EXPOSE 25565-25575

# Health check
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -sf http://localhost:8080/health || exit 1

# Run as non-root user
USER rsc

# Entry point
ENTRYPOINT ["rsc"]
