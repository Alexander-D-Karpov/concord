package discovery

import (
	"context"
	"fmt"
	"net"
	"testing"

	registryv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/registry/v1"
	"github.com/Alexander-D-Karpov/concord/internal/registry"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// metadata keys must match the API-side interceptor's expectations.
func TestMetadataKeysMatchRegistry(t *testing.T) {
	if secretMetadataKey != registry.SecretMetadataKey {
		t.Fatalf("secret metadata key drift: %q vs %q", secretMetadataKey, registry.SecretMetadataKey)
	}
}

type fakeRegistry struct {
	registryv1.UnimplementedRegistryServiceServer
	serverSecret    string
	gotSharedSecret string
	gotHeartbeatID  string
}

func (f *fakeRegistry) RegisterServer(_ context.Context, req *registryv1.RegisterServerRequest) (*registryv1.RegisterServerResponse, error) {
	f.gotSharedSecret = req.SharedSecret
	return &registryv1.RegisterServerResponse{Server: req.Server}, nil
}

func (f *fakeRegistry) Heartbeat(ctx context.Context, _ *registryv1.HeartbeatRequest) (*registryv1.EmptyResponse, error) {
	f.gotHeartbeatID = registry.AuthenticatedServerID(ctx)
	return &registryv1.EmptyResponse{}, nil
}

// VerifyServerSecret satisfies registry.SecretVerifier for the interceptor.
func (f *fakeRegistry) VerifyServerSecret(_ context.Context, _, secret string) error {
	if secret != f.serverSecret {
		return fmt.Errorf("bad server secret")
	}
	return nil
}

func newTestRegistrar(t *testing.T, registerSecret, serverSecret string) (*Registrar, *fakeRegistry) {
	t.Helper()
	fake := &fakeRegistry{serverSecret: serverSecret}

	interceptor := registry.NewMachineAuthInterceptor(fake, registerSecret)
	srv := grpc.NewServer(grpc.UnaryInterceptor(interceptor.Unary()))
	registryv1.RegisterRegistryServiceServer(srv, fake)

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() })
	r, err := NewRegistrar(
		"passthrough:///bufnet",
		"11111111-1111-1111-1111-111111111111",
		"concord-voice", "ru-west-1", "1.2.3.4:50000", "1.2.3.4:9001",
		1000, registerSecret, serverSecret, zap.NewNop(), dialer,
	)
	if err != nil {
		t.Fatalf("NewRegistrar: %v", err)
	}
	return r, fake
}

func TestRegistrarRegisterAndHeartbeatAuthenticate(t *testing.T) {
	r, fake := newTestRegistrar(t, "reg-secret", "srv-secret")
	ctx := context.Background()

	if err := r.Register(ctx); err != nil {
		t.Fatalf("Register should authenticate with x-voice-secret metadata: %v", err)
	}
	if fake.gotSharedSecret != "srv-secret" {
		t.Fatalf("register must send the per-server secret as SharedSecret, got %q", fake.gotSharedSecret)
	}

	stats := func() (int32, int32, float64, float64) { return 0, 0, 0, 0 }
	if err := r.sendHeartbeat(ctx, stats); err != nil {
		t.Fatalf("heartbeat should authenticate: %v", err)
	}
	if fake.gotHeartbeatID != r.serverID {
		t.Fatalf("heartbeat must authenticate as server id %q, got %q", r.serverID, fake.gotHeartbeatID)
	}
}

func TestRegistrarWrongRegisterSecretRejected(t *testing.T) {
	r, _ := newTestRegistrar(t, "reg-secret", "srv-secret")
	r.registerSecret = "wrong"
	if err := r.Register(context.Background()); err == nil {
		t.Fatal("register with wrong secret must be rejected by the interceptor")
	}
}
