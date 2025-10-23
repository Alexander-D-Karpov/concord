# Concord - Discord-like Voice & Chat Platform

A high-performance, production-ready voice and chat platform built with Go, featuring real-time messaging, voice communication, and comprehensive administrative controls.

## Features

### Core Features
- **JWT-based Authentication** with refresh tokens
- **OAuth 2.0 Integration** (Google, GitHub)
- **Real-time Chat** with message persistence
- **Voice Communication** over UDP with encryption
- **Room Management** with role-based access control
- **Redis Caching** for performance optimization
- **Rate Limiting** to prevent abuse
- **Admin Controls** (kick, ban, mute)

### Technical Features
- **gRPC API** with bidirectional streaming
- **PostgreSQL** with Snowflake IDs for message ordering
- **ChaCha20-Poly1305** encryption for voice
- **SFU-style** media forwarding
- **Comprehensive logging** with structured output
- **Metrics & Observability** with Prometheus
- **Docker support** with docker-compose
- **CI/CD** with GitHub Actions

## Architecture

```
┌─────────────────┐         ┌─────────────────┐
│  Native Client  │ ◄─────► │  Main Backend   │
│ (Desktop/Mobile)│  gRPC   │    (Go)         │
│                 │         │  Auth, Rooms    │
│  - UI           │         │  Chat, Admin    │
│  - gRPC stream  │         │                 │
│  - UDP media    │         └────────┬────────┘
└────────┬────────┘                  │
         │                           │ PostgreSQL
         │ UDP media            Redis Cache
         │ (encrypted)               │
         ▼                           ▼
┌─────────────────┐         ┌─────────────────┐
│  Voice CDN      │         │   PostgreSQL    │
│   (Go, UDP)     │         │   Messages      │
│  - SFU relay    │         │   Rooms         │
│  - Encryption   │         │   Users         │
│  - QoS          │         └─────────────────┘
└─────────────────┘
```

### Components

#### Main Backend (`concord-api`)
- gRPC server for all API operations
- JWT authentication with OAuth support
- Real-time event streaming
- Message persistence with Snowflake IDs
- Redis caching for frequently accessed data
- Rate limiting per user/IP
- Admin operations

#### Voice CDN (`concord-voice`)
- UDP-based media relay
- ChaCha20-Poly1305 encryption
- SFU (Selective Forwarding Unit) architecture
- Audio-first QoS
- Session management
- Standalone operation

## Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL 14+
- Redis 7+ (optional)
- Protocol Buffers compiler
- Docker (optional)

### Setup

1. **Clone the repository**
```bash
git clone https://github.com/Alexander-D-Karpov/concord
cd concord
```

2. **Install dependencies**
```bash
make deps
make install-tools
```

3. **Generate protobuf code**
```bash
make proto
```

4. **Configure environment**
```bash
cp .env.example .env
```

5. **Start dependencies**
```bash
docker-compose -f deploy/docker-compose.yml up -d postgres redis

# Or install PostgreSQL and Redis locally
```

6. **Build and run**
```bash
make build

# Run API
./bin/concord-api

# Run Voice (in another terminal)
./bin/concord-voice
```

## Configuration

### Main API Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `GRPC_PORT` | gRPC server port | `9090` |
| `DB_HOST` | PostgreSQL host | `localhost` |
| `DB_PORT` | PostgreSQL port | `5432` |
| `DB_NAME` | Database name | `concord` |
| `JWT_SECRET` | JWT signing secret | Change in production |
| `REDIS_HOST` | Redis host | `localhost` |
| `REDIS_ENABLED` | Enable Redis caching | `true` |
| `RATE_LIMIT_ENABLED` | Enable rate limiting | `true` |
| `RATE_LIMIT_REQUESTS_PER_MINUTE` | Rate limit | `60` |

### Voice Server Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `VOICE_UDP_HOST` | UDP bind address | `0.0.0.0` |
| `VOICE_UDP_PORT_START` | Starting UDP port | `50000` |
| `VOICE_REGION` | Server region | `ru-west` |
| `VOICE_PUBLIC_HOST` | Public hostname/IP | Auto-detected |
| `REGISTRY_URL` | Main API address | `localhost:9090` |

## Monitoring

### Prometheus Metrics

The application exposes Prometheus metrics on:
- API: `http://localhost:9090/metrics`
- Voice: `http://localhost:9091/metrics`

## Deployment

### Docker

```bash
make docker-build

docker-compose -f deploy/docker-compose.yml up -d
```

### Manual Deployment

1. Build binaries: `make build`
2. Set up PostgreSQL and Redis
3. Configure environment variables
4. Run migrations (automatic on startup)
5. Start services

### Environment Variables for Production

```bash
# Security
JWT_SECRET=<strong-random-secret>
VOICE_JWT_SECRET=<another-strong-secret>
VOICE_SECRET=<voice-server-secret>

DB_HOST=your-postgres-host
DB_PASSWORD=<strong-password>

REDIS_HOST=your-redis-host
REDIS_PASSWORD=<redis-password>

LOG_LEVEL=info
LOG_FORMAT=json
```

## API Documentation

### Authentication

```protobuf
// Register a new user
rpc Register(RegisterRequest) returns (Token);

// Login with password
rpc LoginPassword(LoginPasswordRequest) returns (Token);

// Login with OAuth
rpc LoginOAuth(LoginOAuthRequest) returns (Token);

// Refresh access token
rpc Refresh(RefreshRequest) returns (Token);

// Logout
rpc Logout(LogoutRequest) returns (EmptyResponse);
```

### Rooms & Chat

```protobuf
// Create a room
rpc CreateRoom(CreateRoomRequest) returns (Room);

// Send a message
rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);

// Stream events
rpc EventStream(stream ClientEvent) returns (stream ServerEvent);
```

### Voice

```protobuf
// Join voice channel
rpc JoinVoice(JoinVoiceRequest) returns (JoinVoiceResponse);

// Leave voice channel
rpc LeaveVoice(LeaveVoiceRequest) returns (EmptyResponse);
```

### Admin

```protobuf
// Kick user from room
rpc Kick(KickRequest) returns (EmptyResponse);

// Ban user from room
rpc Ban(BanRequest) returns (EmptyResponse);

// Mute user in voice
rpc Mute(MuteRequest) returns (EmptyResponse);
```
