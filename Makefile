include $(wildcard .env)
export

TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/comparai_test?sslmode=disable

.PHONY: run seed test test-integration lint build up down

run:              ## sobe a API local (precisa de Postgres e .env)
	go run ./cmd/api

seed:             ## popula o banco com dados de demonstração
	go run ./cmd/seed

build:
	CGO_ENABLED=0 go build -o bin/api ./cmd/api
	CGO_ENABLED=0 go build -o bin/seed ./cmd/seed

test:             ## testes unitários
	go test ./...

test-integration: ## inclui o teste ponta a ponta (APAGA o schema do TEST_DATABASE_URL)
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -p 1 -race -count=1 ./...   # -p 1: os testes de integração compartilham o banco

lint:
	gofmt -l .
	go vet ./...

up:               ## Postgres + API via Docker; depois rode: docker compose exec api /seed
	docker compose up -d --build

down:
	docker compose down
