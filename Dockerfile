# Build stage
FROM docker.1ms.run/library/golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /bin/plugin ./cmd/plugin

# Final stage
FROM docker.1ms.run/library/alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/plugin /bin/plugin
COPY --from=builder /app/public.pem /app/public.pem

RUN adduser -D -u 1000 plugin
USER plugin

EXPOSE 8080

ENTRYPOINT ["/bin/plugin", "-public-key", "/app/public.pem"]
