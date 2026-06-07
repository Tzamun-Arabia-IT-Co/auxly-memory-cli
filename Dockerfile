# syntax=docker/dockerfile:1
#
# Auxly MCP server image — built for Glama introspection checks.
# Glama starts this container and sends an MCP `initialize` + `tools/list`
# over stdio; the server must boot and respond without any external setup.
#
# Pure-Go build (modernc.org/sqlite), so CGO_ENABLED=0 and no C toolchain.

# ---- build stage ------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache deps first for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

# Build the static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/auxly .

# ---- runtime stage ----------------------------------------------------------
FROM alpine:3.20

# Non-root user + a writable vault path so the server starts with no host setup.
RUN addgroup -S auxly && adduser -S auxly -G auxly
COPY --from=build /out/auxly /usr/local/bin/auxly

# The vault lives here; Glama only needs the server to boot + introspect, and an
# empty vault is fine — the tools list does not require existing memory files.
ENV AUXLY_MEMORY_PATH=/data
RUN mkdir -p /data && chown auxly:auxly /data

USER auxly
WORKDIR /data

# stdio JSON-RPC MCP server (the same entrypoint Claude Desktop uses).
ENTRYPOINT ["auxly", "mcp-server"]
