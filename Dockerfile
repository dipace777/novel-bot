# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -o /out/healthcheck ./cmd/healthcheck \
 && CGO_ENABLED=0 go build -trimpath -o /out/fixtures ./cmd/fixtures

FROM debian:bookworm-slim AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 novelbot && useradd --uid 10001 --gid novelbot --create-home novelbot
COPY --from=build /out/healthcheck /usr/local/bin/healthcheck
WORKDIR /home/novelbot
USER novelbot

FROM base AS api
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["api"]

FROM base AS migrate
COPY --from=build /out/migrate /usr/local/bin/migrate
ENTRYPOINT ["migrate"]

FROM base AS worker
USER root
RUN apt-get update && apt-get install -y --no-install-recommends chromium fonts-liberation \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/worker /usr/local/bin/worker
USER novelbot
ENV CHROMIUM_PATH=/usr/bin/chromium
EXPOSE 8090
ENTRYPOINT ["worker"]

FROM base AS fixtures
COPY --from=build /out/fixtures /usr/local/bin/fixtures
EXPOSE 8082
ENTRYPOINT ["fixtures"]
