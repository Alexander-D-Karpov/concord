.PHONY: proto build clean clean-proto-deps test test-unit test-integration test-coverage test-cleanup \
        run-api run-voice lint deps install-tools all \
        up down restart rebuild ps logs logs-api logs-voice logs-db logs-redis \
        update pull images

ifneq (,$(wildcard ./.env))
    include .env
    export
endif

COMPOSE_FILE := deploy/docker-compose.yml
ENV_FILE     := .env
PROJECT := concord
DC := docker compose -p $(PROJECT) --env-file $(ENV_FILE) -f $(COMPOSE_FILE)

proto:
	@sh scripts/gen_proto.sh

build:
	@echo "Building concord-api..."
	@go build -o bin/concord-api ./cmd/concord-api
	@echo "Building concord-voice..."
	@go build -o bin/concord-voice ./cmd/concord-voice

clean:
	@rm -rf bin/ api/gen/

clean-proto-deps:
	@rm -rf api/proto-deps/

test-cleanup:
	@echo "Resetting test database..."
	@PGPASSWORD=postgres psql -h localhost -U postgres -d postgres -c "DROP DATABASE IF EXISTS concord_test;" 2>/dev/null || true
	@PGPASSWORD=postgres psql -h localhost -U postgres -d postgres -c "CREATE DATABASE concord_test;" 2>/dev/null || true

test: test-cleanup
	@go test -v -race ./...

test-unit: test-cleanup
	@go test -v -race ./tests/unit/...

test-integration: test-cleanup
	@go test -v -race ./tests/integration/...

test-coverage:
	@go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html

run-api:
	@go run ./cmd/concord-api

run-voice:
	@go run ./cmd/concord-voice

lint:
	@golangci-lint run ./...

deps:
	@go mod download
	@go mod tidy

install-tools:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
	@go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@latest
	@go install github.com/bufbuild/buf/cmd/buf@v1.28.1

all: deps proto build


up:
	@echo "Starting docker stack..."
	@$(DC) up -d
	@$(DC) ps

down:
	@echo "Stopping docker stack..."
	@$(DC) down

restart:
	@echo "Restarting docker stack..."
	@$(DC) restart
	@$(DC) ps

rebuild:
	@echo "Rebuilding and restarting docker stack..."
	@$(DC) up -d --build --force-recreate
	@$(DC) ps

ps:
	@$(DC) ps

logs:
	@$(DC) logs -f --tail=200

logs-api:
	@$(DC) logs -f --tail=200 api

logs-voice:
	@$(DC) logs -f --tail=200 voice

logs-db:
	@$(DC) logs -f --tail=200 postgres

logs-redis:
	@$(DC) logs -f --tail=200 redis

pull:
	@echo "Pulling images..."
	@$(DC) pull

images:
	@$(DC) build

update:
	@echo "Updating from git and redeploying..."
	@git pull
	@$(DC) up -d --build --force-recreate
	@$(DC) ps
