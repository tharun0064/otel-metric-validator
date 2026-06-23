# ---- build ----
FROM golang:1.23 AS build
WORKDIR /src

# Deps first for layer caching
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o /out/validator ./cmd/validator

# ---- runtime ----
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/validator /usr/local/bin/validator

# Default: run as a service. Override for one-shot, e.g.
#   docker compose run --rm validator            (one-shot)
#   docker compose run --rm validator --json      (one-shot json)
ENTRYPOINT ["/usr/local/bin/validator"]
CMD ["--watch"]
