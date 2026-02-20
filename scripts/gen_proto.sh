#!/usr/bin/env sh
set -e

echo "Generating protobuf code..."

cd api/proto

PROTO_DEPS_DIR="../proto-deps"

if [ ! -d "$PROTO_DEPS_DIR/googleapis" ]; then
    echo "Downloading googleapis protos..."
    mkdir -p "$PROTO_DEPS_DIR"
    git clone --depth 1 https://github.com/googleapis/googleapis.git "$PROTO_DEPS_DIR/googleapis" 2>/dev/null || true
fi

if [ ! -d "$PROTO_DEPS_DIR/grpc-gateway" ]; then
    echo "Downloading grpc-gateway protos..."
    git clone --depth 1 https://github.com/grpc-ecosystem/grpc-gateway.git "$PROTO_DEPS_DIR/grpc-gateway" 2>/dev/null || true
fi

if [ ! -d "$PROTO_DEPS_DIR/protobuf" ]; then
    echo "Downloading protobuf well-known types..."
    git clone --depth 1 https://github.com/protocolbuffers/protobuf.git "$PROTO_DEPS_DIR/protobuf" 2>/dev/null || true
fi

mkdir -p ../gen/go
mkdir -p ../gen/openapiv2

INCLUDES="-I. \
-I$PROTO_DEPS_DIR/googleapis \
-I$PROTO_DEPS_DIR/grpc-gateway \
-I$PROTO_DEPS_DIR/protobuf/src"

for dir in common/v1 auth/v1 users/v1 rooms/v1 membership/v1 chat/v1 call/v1 registry/v1 admin/v1 friends/v1 dm/v1; do
    echo "Generating for $dir..."

    protoc $INCLUDES \
        --go_out=../gen/go \
        --go_opt=paths=source_relative \
        --go-grpc_out=../gen/go \
        --go-grpc_opt=paths=source_relative \
        --grpc-gateway_out=../gen/go \
        --grpc-gateway_opt=paths=source_relative \
        --grpc-gateway_opt=generate_unbound_methods=true \
        "$dir"/*.proto
done

echo "Generating for stream/v1..."
protoc $INCLUDES \
    --go_out=../gen/go \
    --go_opt=paths=source_relative \
    --go-grpc_out=../gen/go \
    --go-grpc_opt=paths=source_relative \
    stream/v1/*.proto

echo "Generating OpenAPI spec..."
protoc $INCLUDES \
    --openapiv2_out=../gen/openapiv2 \
    --openapiv2_opt=allow_merge=true \
    --openapiv2_opt=merge_file_name=concord \
    --openapiv2_opt=openapi_naming_strategy=fqn \
    auth/v1/*.proto \
    users/v1/*.proto \
    rooms/v1/*.proto \
    membership/v1/*.proto \
    chat/v1/*.proto \
    call/v1/*.proto \
    friends/v1/*.proto \
    dm/v1/*.proto \
    admin/v1/*.proto

echo "Protobuf generation complete!"