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

STREAM_DIRS="stream"
SKIP_OPENAPI_DIRS="stream common"

GATEWAY_DIRS=""
OPENAPI_FILES=""

for dir in */v1; do
    [ -d "$dir" ] || continue
    ls "$dir"/*.proto >/dev/null 2>&1 || continue

    SERVICE=$(echo "$dir" | cut -d/ -f1)

    echo "Generating for $dir..."

    if echo "$STREAM_DIRS" | grep -qw "$SERVICE"; then
        protoc $INCLUDES \
            --go_out=../gen/go \
            --go_opt=paths=source_relative \
            --go-grpc_out=../gen/go \
            --go-grpc_opt=paths=source_relative \
            "$dir"/*.proto
    else
        protoc $INCLUDES \
            --go_out=../gen/go \
            --go_opt=paths=source_relative \
            --go-grpc_out=../gen/go \
            --go-grpc_opt=paths=source_relative \
            --grpc-gateway_out=../gen/go \
            --grpc-gateway_opt=paths=source_relative \
            --grpc-gateway_opt=generate_unbound_methods=true \
            "$dir"/*.proto
    fi

    if ! echo "$SKIP_OPENAPI_DIRS" | grep -qw "$SERVICE"; then
        OPENAPI_FILES="$OPENAPI_FILES $dir/*.proto"
    fi
done

if [ -n "$OPENAPI_FILES" ]; then
    echo "Generating OpenAPI spec..."
    eval protoc $INCLUDES \
        --openapiv2_out=../gen/openapiv2 \
        --openapiv2_opt=allow_merge=true \
        --openapiv2_opt=merge_file_name=concord \
        --openapiv2_opt=openapi_naming_strategy=fqn \
        $OPENAPI_FILES
fi

echo "Protobuf generation complete!"