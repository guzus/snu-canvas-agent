FROM golang:1.24.1-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/lx-agent ./cmd/lx-agent

FROM alpine:3.21
WORKDIR /app
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/lx-agent /app/lx-agent
USER appuser

CMD ["./lx-agent", "serve"]
