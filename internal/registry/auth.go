package registry

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	// SecretMetadataKey is the gRPC metadata header carrying a voice server's shared
	// secret (the registration secret for RegisterServer, the per-server secret for
	// Heartbeat).
	SecretMetadataKey = "x-voice-secret"
	// serverIDMetadataKey is the gRPC metadata header naming the calling voice
	// server's ID, used to look up its stored secret on Heartbeat.
	serverIDMetadataKey = "x-voice-server-id"
)

// serverIDKeyType is the unexported context-key type for the authenticated server ID.
type serverIDKeyType struct{}

// serverIDCtxKey is the context key under which the authenticated server ID is stored.
var serverIDCtxKey = serverIDKeyType{}

// machineMethods is the set of registry RPCs authenticated by shared secret rather
// than user JWT.
var machineMethods = map[string]bool{
	"/concord.registry.v1.RegistryService/RegisterServer": true,
	"/concord.registry.v1.RegistryService/Heartbeat":      true,
}

// SecretVerifier checks a presented per-server secret against the stored hash for a
// server ID, returning an error if the server is unknown or the secret is wrong.
type SecretVerifier interface {
	VerifyServerSecret(ctx context.Context, serverID, secret string) error
}

// MachineAuthInterceptor authenticates machine-to-machine registry RPCs. It gates
// RegisterServer on a shared registerSecret and Heartbeat on the per-server secret
// verified via verifier.
type MachineAuthInterceptor struct {
	verifier       SecretVerifier
	registerSecret string
}

// NewMachineAuthInterceptor returns an interceptor that accepts registrations
// bearing registerSecret and verifies per-server heartbeat secrets through verifier.
// An empty registerSecret disables server registration.
func NewMachineAuthInterceptor(verifier SecretVerifier, registerSecret string) *MachineAuthInterceptor {
	return &MachineAuthInterceptor{verifier: verifier, registerSecret: registerSecret}
}

// HashSecret returns the hex-encoded SHA-256 of secret, the form in which server
// secrets are stored and compared so plaintext secrets are never persisted.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// IsMachineMethod reports whether method is a registry RPC handled by this
// machine-auth interceptor rather than user-JWT auth.
func (m *MachineAuthInterceptor) IsMachineMethod(method string) bool {
	return machineMethods[method]
}

// Authenticate verifies the machine credentials in ctx for the given method. For
// RegisterServer it constant-time compares the presented secret to registerSecret
// (Forbidden if registration is disabled). For Heartbeat it requires a server ID
// and validates its per-server secret via the verifier, returning a context that
// carries the authenticated server ID. Returns Unauthorized/Forbidden on failure.
func (m *MachineAuthInterceptor) Authenticate(ctx context.Context, method string) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, errors.Unauthorized("missing metadata")
	}

	secret := firstMD(md, SecretMetadataKey)
	if secret == "" {
		return nil, errors.Unauthorized("missing voice secret")
	}

	switch method {
	case "/concord.registry.v1.RegistryService/RegisterServer":
		if m.registerSecret == "" {
			return nil, errors.Forbidden("server registration disabled")
		}
		if subtle.ConstantTimeCompare([]byte(secret), []byte(m.registerSecret)) != 1 {
			return nil, errors.Unauthorized("invalid registration secret")
		}
		return ctx, nil

	case "/concord.registry.v1.RegistryService/Heartbeat":
		serverID := firstMD(md, serverIDMetadataKey)
		if serverID == "" {
			return nil, errors.Unauthorized("missing server id")
		}
		if err := m.verifier.VerifyServerSecret(ctx, serverID, secret); err != nil {
			return nil, err
		}
		return context.WithValue(ctx, serverIDCtxKey, serverID), nil
	}

	return nil, errors.Unauthorized("not a machine method")
}

// Unary returns a unary interceptor that authenticates machine methods via
// Authenticate and passes all other RPCs through untouched (they are handled by the
// user-auth interceptor). A machine method failing auth is rejected with a gRPC error.
func (m *MachineAuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !m.IsMachineMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		newCtx, err := m.Authenticate(ctx, info.FullMethod)
		if err != nil {
			return nil, errors.ToGRPCError(err)
		}
		return handler(newCtx, req)
	}
}

// AuthenticatedServerID returns the server ID established by a successful Heartbeat
// authentication, or "" if the context was not machine-authenticated as a server.
func AuthenticatedServerID(ctx context.Context) string {
	if v, ok := ctx.Value(serverIDCtxKey).(string); ok {
		return v
	}
	return ""
}

// firstMD returns the first value for key in md, or "" if the key is absent.
func firstMD(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
