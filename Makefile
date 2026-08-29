.PHONY: test build lint up down migrate

test:
	go test ./... -race -count=1

build:
	go build -o bin/airlock ./cmd/airlock

lint:
	go vet ./...

up:
	docker compose --env-file .env -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

migrate:
	go run ./cmd/airlock migrate
