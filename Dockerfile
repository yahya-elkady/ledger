# Multi-stage build → small, non-root, static image.
FROM golang:1.26-alpine AS builder
WORKDIR /app
# Cache the module layer separately from the source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary (CGO off), stripped and trimmed for a smaller, reproducible image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server ./cmd/server

# distroless :nonroot runs as an unprivileged user (uid 65532) with no shell or
# package manager — minimal attack surface. TLS is terminated at the proxy, so
# the server speaks plain HTTP internally on 8080.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
