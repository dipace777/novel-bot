SQLC_VERSION := v1.31.1
SQLC := $(CURDIR)/bin/sqlc

.PHONY: run build test vet db redis migrate tools generate sqlc-check loadtest run-api run-worker up down capacity

run: run-api

run-api:
	./scripts/with-env.sh go run ./cmd/api

run-worker:
	./scripts/with-env.sh go run ./cmd/worker

build:
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker
	go build -o bin/healthcheck ./cmd/healthcheck
	go build -o bin/migrate ./cmd/migrate
	go build -o bin/loadtest ./cmd/loadtest
	go build -o bin/fixtures ./cmd/fixtures

test:
	go test -race ./...

vet:
	go vet ./...

db:
	docker compose up -d --wait postgres redis

redis:
	docker compose up -d --wait redis

migrate:
	./scripts/with-env.sh go run ./cmd/migrate

tools:
	@if [ ! -x "$(SQLC)" ] || [ "$$("$(SQLC)" version)" != "$(SQLC_VERSION)" ]; then \
		GOBIN="$(CURDIR)/bin" go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION); \
	fi

generate: tools
	"$(SQLC)" generate

sqlc-check: tools
	"$(SQLC)" compile
	"$(SQLC)" diff

# Example: make loadtest LOADTEST_ARGS='--concurrency 1,2,4 --metrics-urls http://localhost:8090 --worker-memory-mib 2048'
loadtest:
	./scripts/with-env.sh go run ./cmd/loadtest $(LOADTEST_ARGS)

# The app profile adds one public API, migrations, and two independent workers.
up:
	docker compose --profile app up -d --build --wait

down:
	docker compose --profile app down

# Isolated worker resource sizing; generated reports go to loadtest-results/.
capacity:
	go build -o bin/loadtest ./cmd/loadtest
	python3 scripts/capacity.py $(CAPACITY_ARGS)
