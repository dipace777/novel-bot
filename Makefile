.PHONY: run build test vet db migrate

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
