FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY hy2-reserve/ ./hy2-reserve/
COPY hysteria-relay/ ./hysteria-relay/
WORKDIR /src/hy2-reserve
RUN go mod tidy && CGO_ENABLED=0 go build -o /node ./cmd/node

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /node /usr/local/bin/node
ENTRYPOINT ["node"]
CMD ["-config", "/etc/hy2-reserve/config.yaml"]
