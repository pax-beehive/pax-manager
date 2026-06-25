# syntax=docker/dockerfile:1

FROM golang:1.25 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=secret,id=github_token,required=false \
    set -eu; \
    token_file=/run/secrets/github_token; \
    if [ -s "$token_file" ]; then \
        token="$(cat "$token_file")"; \
        git config --global url."https://x-access-token:${token}@github.com/".insteadOf "https://github.com/"; \
    fi; \
    GOPRIVATE=github.com/pax-beehive/* go mod download; \
    if [ -s "$token_file" ]; then \
        git config --global --unset-all url."https://x-access-token:${token}@github.com/".insteadOf; \
    fi

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/pax-manager ./cmd/manager

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/pax-manager /app/pax-manager
COPY db /app/db
COPY static /app/static

EXPOSE 9879

USER nonroot:nonroot

ENTRYPOINT ["/app/pax-manager"]
