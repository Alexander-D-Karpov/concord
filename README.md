# Concord

A high-performance voice and chat platform built with Go, featuring real-time messaging and voice/video communication.

## Features

- **Authentication** - JWT with refresh tokens, OAuth 2.0 (Google, GitHub)
- **Real-time Chat** - Messages with persistence, reactions, threads, pins
- **Voice Communication** - UDP-based with ChaCha20-Poly1305 encryption
- **Direct Messages** - Private conversations with read receipts
- **Room Management** - Role-based permissions, invites, admin controls
- **Friend System** - Requests, blocking, presence status
- **Read Tracking** - Efficient unread count tracking using Snowflake IDs
- **Typing Indicators** - Real-time typing status in rooms and DMs
- **Redis Caching** - Performance optimization and rate limiting
- **Prometheus Metrics** - Full observability

## Architecture
```
┌─────────────────┐         ┌─────────────────┐
│  Native Client  │ ◄─────► │  Main Backend   │
│ (Desktop/Mobile)│  gRPC   │  (concord-api)  │
│                 │         │                 │
│  - UI           │         │  - Auth         │
│  - gRPC stream  │         │  - Rooms/Chat   │
│  - UDP media    │         │  - DMs/Friends  │
└────────┬────────┘         └────────┬────────┘
         │                           │
         │ UDP                  PostgreSQL
         │ (encrypted)          Redis
         ▼                           │
┌─────────────────┐                  │
│  Voice Server   │                  │
│ (concord-voice) │                  │
│                 │                  │
│  - SFU relay    │◄─────────────────┘
│  - Encryption   │     Registry
└─────────────────┘
```

### Components

#### Main Backend (concord-api)
- gRPC server for all API operations
- JWT authentication with OAuth support
- Real-time event streaming via bidirectional gRPC
- Message persistence with Snowflake IDs
- Redis caching for frequently accessed data
- Efficient read tracking using last-read positions

#### Voice Server (concord-voice)
- UDP-based media relay
- ChaCha20-Poly1305 encryption
- SFU (Selective Forwarding Unit) architecture
- Audio-first QoS
- Session management

## Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL 14+
- Redis 7+
- Protocol Buffers compiler

### Setup
```bash
# Clone repository
git clone https://github.com/Alexander-D-Karpov/concord
cd concord

# Install dependencies
make deps
make install-tools
make proto

# Configure environment
cp .env.example .env
# Edit .env with your settings

# Start infrastructure
docker-compose -f deploy/docker-compose.yml up -d postgres redis

# Build and run
make build
./bin/concord-api
./bin/concord-voice
```

## Configuration

### Main API

| Variable | Description | Default |
|----------|-------------|---------|
| `GRPC_PORT` | gRPC server port | `9000` |
| `SERVER_PORT` | HTTP gateway port | `8080` |
| `DB_HOST` | PostgreSQL host | `localhost` |
| `DB_PORT` | PostgreSQL port | `5432` |
| `DB_NAME` | Database name | `concord` |
| `JWT_SECRET` | JWT signing secret | Required |
| `REDIS_HOST` | Redis host | `localhost` |
| `REDIS_ENABLED` | Enable Redis caching | `true` |
| `RATE_LIMIT_ENABLED` | Enable rate limiting | `true` |
| `RATE_LIMIT_REQUESTS_PER_MINUTE` | Rate limit | `120` |

### Voice Server

| Variable | Description | Default |
|----------|-------------|---------|
| `VOICE_UDP_HOST` | UDP bind address | `0.0.0.0` |
| `VOICE_UDP_PORT_START` | Starting UDP port | `50000` |
| `VOICE_REGION` | Server region | `ru-west-1` |
| `VOICE_PUBLIC_HOST` | Public hostname/IP | Auto-detected |
| `REGISTRY_URL` | Main API address | `localhost:9000` |

## Development
```bash
make test          # Run all tests
make test-unit     # Run unit tests
make test-integration # Run integration tests
make lint          # Run linter
make build         # Build binaries
make proto         # Generate protobuf
make docker-build  # Build Docker images
```

## Deployment

### Docker
```bash
make docker-build
docker-compose -f deploy/docker-compose.yml up -d
```

### Production Configuration
```bash
# Required secrets
JWT_SECRET=<strong-random-secret>
VOICE_JWT_SECRET=<another-strong-secret>
VOICE_SECRET=<voice-server-secret>

# Database
DB_HOST=your-postgres-host
DB_PASSWORD=<strong-password>

# Redis
REDIS_HOST=your-redis-host
REDIS_PASSWORD=<redis-password>

# Logging
LOG_LEVEL=info
LOG_FORMAT=json
```

## Monitoring

| Endpoint | Description |
|----------|-------------|
| `:9100/metrics` | API Prometheus metrics |
| `:8081/health` | API health check |
| `:9101/metrics` | Voice server metrics |
| `:8082/health` | Voice health check |


## License

MIT