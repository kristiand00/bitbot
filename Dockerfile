# --- Builder Stage ---
FROM golang:1.24.1-alpine AS builder

ENV GOOS=linux
ENV GOARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download && go mod verify

COPY . .

RUN go build -ldflags="-w -s" -o /app/bitbot ./main.go

# --- Final Stage ---
FROM alpine:3.20

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Install tzdata and set the time zone
RUN apk add --no-cache tzdata \
    && cp /usr/share/zoneinfo/Europe/Zagreb /etc/localtime \
    && echo "Europe/Zagreb" > /etc/timezone
ENV TZ=Europe/Zagreb

WORKDIR /app

COPY --from=builder /app/bitbot /app/bitbot

RUN mkdir -p /app/pb_data && chown appuser:appgroup /app/pb_data

VOLUME /app/pb_data

EXPOSE 8090

USER appuser

ENTRYPOINT ["/app/bitbot", "serve-with-bot"]
