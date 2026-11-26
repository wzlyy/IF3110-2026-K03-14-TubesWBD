# syntax=docker/dockerfile:1
FROM node:22-alpine AS frontend
WORKDIR /src
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server && CGO_ENABLED=0 go build -o /out/seed ./cmd/seed

FROM builder AS worker-build
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

FROM runtime AS worker
COPY --from=worker-build /out/worker ./worker
ENTRYPOINT ["/app/worker"]

FROM runtime AS api
COPY --from=builder /out/server /out/seed ./
COPY --from=frontend /src/dist ./frontend/dist
EXPOSE 8080
ENTRYPOINT ["/app/server"]
