FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /discuss-hub ./cmd/server

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN groupadd --gid 10001 forum \
    && useradd --uid 10001 --gid forum --no-create-home --shell /usr/sbin/nologin forum \
    && mkdir -p /app/data \
    && chown forum:forum /app/data
WORKDIR /app
COPY --from=build /discuss-hub /app/discuss-hub
ENV ADDR=:8080 DB_PATH=/app/data/forum.db COOKIE_SECURE=false
USER 10001:10001
EXPOSE 8080
VOLUME ["/app/data"]
ENTRYPOINT ["/app/discuss-hub"]
