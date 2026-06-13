# syntax=docker/dockerfile:1

FROM golang:1.25 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

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
