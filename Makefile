SQLC_VERSION := v1.31.1
SQLC := $(CURDIR)/bin/sqlc

.PHONY: run build test vet db migrate tools generate sqlc-check

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api
	go build -o bin/migrate ./cmd/migrate

test:
	go test -race ./...

vet:
	go vet ./...

db:
	docker compose up -d --wait postgres

migrate:
	go run ./cmd/migrate

tools:
	@if [ ! -x "$(SQLC)" ] || [ "$$("$(SQLC)" version)" != "$(SQLC_VERSION)" ]; then \
		GOBIN="$(CURDIR)/bin" go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION); \
	fi

generate: tools
	"$(SQLC)" generate

sqlc-check: tools
	"$(SQLC)" compile
	"$(SQLC)" diff
