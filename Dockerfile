# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

FROM alpine:3.21 AS api
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /out/server /app/server
COPY migrations /app/migrations
COPY web /app/web

EXPOSE 8080
ENTRYPOINT ["/app/server"]
