# --- Builder Stage ---
FROM golang:1.24.1-alpine AS builder

# Set necessary environment variables for building
ENV CGO_ENABLED=1 # Required for gopus
ENV GOOS=linux
ENV GOARCH=amd64

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker cache
COPY go.mod go.sum ./
# Install build-time dependencies for CGO (if pion/opus uses it)
# opus-dev is removed as pion/opus might be pure Go or self-contained CGo.
RUN apk add --no-cache gcc musl-dev
RUN go mod download
RUN go mod verify

# Copy the rest of the application source code
COPY . .

# Compile the application
# -ldflags="-w -s" makes the binary smaller by stripping debug information
RUN go build -ldflags="-w -s" -o /app/bitbot ./main.go

# --- Final Stage ---
FROM alpine:latest

# Install run-time dependencies
# opus is needed by pion/opus (dynamic linking)
RUN apk add --no-cache opus

# Create a non-root user and group
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Set working directory
WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/bitbot /app/bitbot

# Copy PocketBase data directory structure (optional, if you want to pre-populate, but VOLUME handles runtime data)
# If pb_data contains essential bootstrap files created by pocketbase.New() + Bootstrap(),
# it might be better to ensure the directory exists and permissions are set,
# and let the application create the initial DB files on first run if they are not in the volume.
# For now, just ensure the directory is created for the volume.
RUN mkdir -p /app/pb_data && chown appuser:appgroup /app/pb_data

# Define the volume for PocketBase data
# This path should match where PocketBase is configured to store its data.
# Assuming PocketBase defaults to "pb_data" in the current working directory of the app.
VOLUME /app/pb_data

# Expose any ports if the application listens on them (not directly applicable for this Discord bot)
# EXPOSE 8080

# Set the user for running the application
USER appuser

# Environment variables that need to be set at runtime
# These are examples; actual values will be provided when running the container.
ENV BOT_TOKEN=""
ENV GEMINI_API_KEY=""
ENV CRYPTO_TOKEN=""
ENV APP_ID=""
ENV ADMIN_DISCORD_ID=""
# PocketBase specific env vars if needed (e.g., for data directory, though it defaults to pb_data)
# ENV PB_DATA_DIR="/app/pb_data"

# Command to run the application
ENTRYPOINT ["/app/bitbot"]
