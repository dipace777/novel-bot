# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.27.1-bookworm@sha256:a4f46dc39c6b0359a3e1ed86ef14d01b374cc808649679dd5fca2290e6d54202
ARG BASE_IMAGE=debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/healthcheck ./cmd/healthcheck \
 && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/fixtures ./cmd/fixtures

FROM ${BASE_IMAGE} AS base
ARG DEBIAN_SNAPSHOT=20261006T000000Z
# Bootstrap TLS trust from the pinned build image; apt still verifies signatures.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* \
 && printf 'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/%s bookworm main\ndeb [check-valid-until=no] https://snapshot.debian.org/archive/debian-security/%s bookworm-security main\n' "$DEBIAN_SNAPSHOT" "$DEBIAN_SNAPSHOT" > /etc/apt/sources.list
RUN apt-get update && apt-get upgrade -y --no-install-recommends \
 && apt-get install -y --no-install-recommends ca-certificates \
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
ARG CHROMIUM_VERSION=154.0.8037.92-1~deb12u1
RUN apt-get update && apt-get install -y --no-install-recommends "chromium=$CHROMIUM_VERSION" fonts-liberation \
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
