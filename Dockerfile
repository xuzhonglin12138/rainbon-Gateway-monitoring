# Build stage
FROM m.daocloud.io/docker.io/library/golang:1.24-alpine AS builder

WORKDIR /app

RUN sed -i 's#https://dl-cdn.alpinelinux.org/alpine#https://mirrors.aliyun.com/alpine#g' /etc/apk/repositories
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download

COPY . .

ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /bin/plugin ./cmd/plugin

# Final stage
FROM m.daocloud.io/docker.io/library/alpine:3.19

RUN sed -i 's#https://dl-cdn.alpinelinux.org/alpine#https://mirrors.aliyun.com/alpine#g' /etc/apk/repositories
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/plugin /bin/plugin
COPY --from=builder /app/public.pem /app/public.pem

RUN adduser -D -u 1000 plugin
USER plugin

EXPOSE 8080

ENTRYPOINT ["/bin/plugin", "-public-key", "/app/public.pem"]
