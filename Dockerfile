############################
# STEP 1 Build base
############################
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.25.8-alpine3.23 AS builder
WORKDIR /build

# Install build dependencies
RUN apk add --no-cache build-base

# Cache dependencies to speed up subsequent builds
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download -x

# Copy source code and build the binary
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -tags musl -o /build/bin/milvus-utils main.go

############################
# STEP 2 Finalize image
############################
FROM --platform=$BUILDPLATFORM docker.io/library/alpine:3.23 AS runtime
WORKDIR /app

# Add runtime dependencies
RUN apk add --no-cache ca-certificates

# Create an unprivileged user and group named 'app'
RUN addgroup -S app && adduser -S app -G app

# Copy the built binary from the builder stage
COPY --from=builder /build/bin/milvus-utils /usr/bin/milvus-utils

# Set permissions and switch to the unprivileged user
RUN chown -R app:app /app
USER app

ENTRYPOINT [ "milvus-utils" ]
