# CI-only replacements for the unavailable Quay images. Keep the releases in
# sync with docker-compose.integration.yml and verify the official artifacts.
FROM alpine:3.22 AS base
RUN apk add --no-cache ca-certificates curl

FROM base AS minio
ARG TARGETARCH
RUN test "$TARGETARCH" = amd64 \
    && curl --fail --location --retry 3 \
      https://github.com/minio/minio/releases/download/RELEASE.2025-09-07T16-13-09Z/minio.linux-amd64.RELEASE.2025-09-07T16-13-09Z \
      --output /usr/local/bin/minio \
    && echo '7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f  /usr/local/bin/minio' | sha256sum -c - \
    && chmod 0755 /usr/local/bin/minio
ENTRYPOINT ["/usr/local/bin/minio"]

FROM base AS mc
ARG TARGETARCH
RUN test "$TARGETARCH" = amd64 \
    && curl --fail --location --retry 3 \
      https://github.com/minio/mc/releases/download/RELEASE.2025-08-13T08-35-41Z/mc.linux-amd64.RELEASE.2025-08-13T08-35-41Z \
      --output /usr/local/bin/mc \
    && echo '01f866e9c5f9b87c2b09116fa5d7c06695b106242d829a8bb32990c00312e891  /usr/local/bin/mc' | sha256sum -c - \
    && chmod 0755 /usr/local/bin/mc
ENTRYPOINT ["/usr/local/bin/mc"]
