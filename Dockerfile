FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN go build -mod=vendor -o /out/omnirepo ./cmd/omnirepo

FROM alpine:3.20
RUN adduser -D -u 1000 omnirepo \
 && mkdir -p /var/lib/omnirepo \
 && chown omnirepo:omnirepo /var/lib/omnirepo
USER 1000
VOLUME ["/var/lib/omnirepo"]
COPY --from=build /out/omnirepo /usr/local/bin/omnirepo
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/omnirepo", "serve"]
