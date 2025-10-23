#!/bin/bash
set -e

echo "Generating protobuf code..."

cd api/proto

mkdir -p ../gen/go

for dir in common/v1 auth/v1 users/v1 rooms/v1 membership/v1 chat/v1 stream/v1 call/v1 registry/v1 admin/v1; do
    protoc \
        --go_out=../gen/go \
        --go_opt=paths=source_relative \
        --go-grpc_out=../gen/go \
        --go-grpc_opt=paths=source_relative \
        -I. \
        "$dir"/*.proto
done

echo "Protobuf generation complete!"
