# payments-service developer tasks.
# Secrets come from the environment / .env — never hardcode them here.

# Load DATABASE_URL (and friends) from .env for migrate targets, if present.
ifneq (,$(wildcard ./.env))
include .env
export
endif

MIGRATIONS_DIR := migrations
SERVER_PKG     := ./cmd/server

.PHONY: run build test test-integration generate lint migrate-up migrate-down seed-test tidy help

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

run: ## Run the HTTP server
	go run $(SERVER_PKG)

build: ## Build the server binary into ./bin
	go build -o bin/server $(SERVER_PKG)

test: ## Run the unit test suite (no Docker needed)
	go test ./...

test-integration: ## Run the dockertest end-to-end suite (requires a Docker daemon)
	go test -tags=integration ./internal/integration/...

generate: ## Regenerate sqlc type-safe query code
	sqlc generate

lint: ## Run golangci-lint (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run

tidy: ## Tidy go.mod / go.sum
	go mod tidy

migrate-up: ## Apply all up migrations (requires golang-migrate CLI + DATABASE_URL)
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the most recent migration
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

seed-test: ## Seed test-mode fixtures (merchant, API key, customer, charges)
	go run ./cmd/seed
