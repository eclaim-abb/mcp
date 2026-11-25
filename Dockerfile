# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/eclaim-mcp .

FROM alpine:3.24 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=build /out/eclaim-mcp /usr/local/bin/eclaim-mcp

ENV ECLAIM_MCP_LISTEN_ADDR=0.0.0.0:8080
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["wget", "-q", "-T", "2", "-O", "/dev/null", "http://127.0.0.1:8080/health"]
ENTRYPOINT ["/usr/local/bin/eclaim-mcp"]