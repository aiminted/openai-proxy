FROM golang:1.25.4-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o openai-proxy ./cmd/openai-proxy

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /build/openai-proxy .
COPY pricing.yaml .

EXPOSE 8080

ENTRYPOINT ["/app/openai-proxy"]
