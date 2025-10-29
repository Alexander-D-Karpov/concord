package cdn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

type HealthChecker struct {
	timeout time.Duration
}

func NewHealthChecker(timeout time.Duration) *HealthChecker {
	return &HealthChecker{timeout: timeout}
}

func (h *HealthChecker) CheckUDP(ctx context.Context, addr string) (int32, error) {
	host, port := h.parseAddr(addr)
	if host == "" {
		return 0, fmt.Errorf("invalid address")
	}

	start := time.Now()
	conn, err := net.DialTimeout("udp", fmt.Sprintf("%s:%s", host, port), h.timeout)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := conn.Close(); err != nil {
			return
		}
	}()

	latency := time.Since(start).Milliseconds()
	return int32(latency), nil
}

func (h *HealthChecker) parseAddr(addr string) (string, string) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func (h *HealthChecker) CheckMultiple(ctx context.Context, addrs []string) map[string]HealthResult {
	results := make(map[string]HealthResult)
	for _, addr := range addrs {
		latency, err := h.CheckUDP(ctx, addr)
		results[addr] = HealthResult{
			Addr:    addr,
			Latency: latency,
			Error:   err,
			Healthy: err == nil,
		}
	}
	return results
}

type HealthResult struct {
	Addr    string
	Latency int32
	Error   error
	Healthy bool
}
