FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /storage-bot .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /storage-bot /usr/local/bin/storage-bot
ENTRYPOINT ["storage-bot"]
CMD ["--config", "/etc/storage-bot/config.yaml"]
