.PHONY: proto build clean test test-unit test-integration run-api run-voice migrate docker-build lint deps all test-cleanup

proto:
	@bash scripts/gen_proto.sh

build:
	@echo "Building concord-api..."
	@go build -o bin/concord-api ./cmd/concord-api
	@echo "Building concord-voice..."
	@go build -o bin/concord-voice ./cmd/concord-voice

clean:
	@rm -rf bin/ api/gen/

test-cleanup:
	@echo "Cleaning test database..."
	@PGPASSWORD=postgres psql -h localhost -U postgres -d concord_test -c "TRUNCATE users, rooms, memberships, messages, refresh_tokens, voice_servers CASCADE;" 2>/dev/null || true

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

docker-build:
	@docker build -f deploy/Dockerfile.api -t concord-api:latest .
	@docker build -f deploy/Dockerfile.voice -t concord-voice:latest .

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

all: proto build