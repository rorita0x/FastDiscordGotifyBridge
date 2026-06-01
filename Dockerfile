# syntax=docker/dockerfile:1

# ---- Stage 1: build ----------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Fetch the Mozilla CA bundle and place it where the certs package embeds it.
# This lets the final scratch image do TLS without shipping any cert files.
ADD https://curl.se/ca/cacert.pem /src/internal/certs/cacert.pem

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source (does not overwrite the fetched cacert.pem).
COPY . .

# Build a fully static, stripped binary with the CA bundle embedded.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -tags embedcerts \
        -trimpath \
        -ldflags="-s -w" \
        -o /bridge ./

# ---- Stage 2: final — absolutely empty, only the binary ----------------------
FROM scratch
COPY --from=build /bridge /bridge
# Config is expected at /config.toml (mount it) or set CONFIG_PATH.
ENTRYPOINT ["/bridge"]
