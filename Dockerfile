# ==============================================================================
# Stage 1: Build SPA (Node)
# ==============================================================================
FROM node:22-alpine AS web-build
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ==============================================================================
# Stage 2: Build Go binary (with embedded SPA)
# ==============================================================================
FROM golang:1.26-alpine AS go-build
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
COPY --from=web-build /web/dist web/dist
ARG VERSION=dev
RUN go build -mod=vendor -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /out/omnirepo ./cmd/omnirepo

# ==============================================================================
# Stage 3: Fetch Trivy binary + bake DB
# ==============================================================================
FROM aquasec/trivy:0.72.0 AS trivy
RUN trivy image --download-db-only --cache-dir /trivy-cache

# ==============================================================================
# Stage 4: Runtime (alpine:3.23)
# ==============================================================================
FROM alpine:3.23
RUN apk add --no-cache git ca-certificates wget \
    && adduser -D -u 1000 omnirepo \
    && mkdir -p /var/lib/omnirepo \
    && chown -R omnirepo:omnirepo /var/lib/omnirepo

COPY --from=go-build /out/omnirepo /usr/local/bin/omnirepo
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy
COPY --from=trivy /trivy-cache/db /opt/trivy-db/

USER 1000
VOLUME ["/var/lib/omnirepo"]
EXPOSE 8080 8443

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1

ENTRYPOINT ["omnirepo", "serve"]
