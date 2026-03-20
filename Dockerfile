FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /hy2scale ./cmd/node

FROM alpine:3.19
RUN apk add --no-cache ca-certificates xl2tpd strongswan ppp iptables iproute2
COPY --from=builder /hy2scale /usr/local/bin/hy2scale
VOLUME /data
ENTRYPOINT ["hy2scale"]
