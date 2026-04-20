# syntax=docker/dockerfile:1

# GoReleaser places the already-built binary in the build context as
# ./escrow-proxy for each target arch; this Dockerfile just wraps it.
FROM gcr.io/distroless/static-debian12:nonroot

COPY escrow-proxy /usr/local/bin/escrow-proxy

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/escrow-proxy"]
