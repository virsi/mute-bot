.PHONY: lint test test-int build docker-up docker-down migrate-up migrate-down

GO ?= go
PKG := ./...
DOCKER_COMPOSE := docker compose -f deploy/docker-compose.yml

lint:
	golangci-lint run

test:
	$(GO) test -race -count=1 -short $(PKG)

test-int:
	$(GO) test -race -count=1 -tags=integration -timeout=10m $(PKG)

build:
	$(GO) build -o bin/session-reader ./cmd/session-reader
	$(GO) build -o bin/processor      ./cmd/processor
	$(GO) build -o bin/bot-api        ./cmd/bot-api
	$(GO) build -o bin/scheduler      ./cmd/scheduler

docker-up:
	$(DOCKER_COMPOSE) up -d

docker-down:
	$(DOCKER_COMPOSE) down -v

migrate-up:
	migrate -path migrations -database "$$POSTGRES_DSN" up

migrate-down:
	migrate -path migrations -database "$$POSTGRES_DSN" down 1
