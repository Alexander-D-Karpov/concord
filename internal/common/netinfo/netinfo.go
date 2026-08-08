package netinfo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Advertised is the set of addresses a server will advertise to clients, along
// with how the public host was determined (Source) and any operator-facing
// Notes (e.g. NAT warnings).
type Advertised struct {
	PublicHost string // user-configured domain or detected public IP (if any)
	LANHost    string // local/LAN IP fallback
	Port       int
	Source     string // "config", "env", "http", "lan"
	Notes      []string
}

// ComputeAdvertised determines the addresses to advertise for port. The public
// host is resolved by precedence: userConfiguredHost, then the
// CONCORD_PUBLIC_HOST env var, then an outbound HTTP call to a public-IP echo
// service. The LAN host is detected via an outbound-route probe with a
// private-IP fallback, ultimately 127.0.0.1. It appends explanatory Notes when
// no public host is found or when udpBindHost is a wildcard. WARNING: this makes
// real network calls (outbound HTTP and a UDP dial), so it can block up to a few
// seconds; ctx bounds the HTTP lookups.
func ComputeAdvertised(ctx context.Context, userConfiguredHost, udpBindHost string, port int) Advertised {
	adv := Advertised{Port: port}

	if h := strings.TrimSpace(userConfiguredHost); h != "" {
		h = trimScheme(h)
		h = stripPort(h)
		adv.PublicHost = h
		adv.Source = "config"
	} else if env := strings.TrimSpace(os.Getenv("CONCORD_PUBLIC_HOST")); env != "" {
		h := trimScheme(env)
		h = stripPort(h)
		adv.PublicHost = h
		adv.Source = "env"
	} else {
		if ip, err := detectPublicIP(ctx); err == nil && ip != "" {
			adv.PublicHost = ip
			adv.Source = "http"
		}
	}

	if lan, err := detectLANIPPreferOutbound(); err == nil && lan != "" {
		adv.LANHost = lan
	} else if lan, err := firstPrivateIPv4(); err == nil && lan != "" {
		adv.LANHost = lan
	} else {
		adv.LANHost = "127.0.0.1"
		adv.Notes = append(adv.Notes, "Could not find a LAN IP; falling back to 127.0.0.1.")
	}

	if adv.PublicHost == "" {
		adv.Source = "lan"
		adv.Notes = append(adv.Notes,
			"No public host detected. If this server is behind NAT, you may need port forwarding.",
			`You can set a domain or public IP via config (e.g., Voice.PublicHost) or env CONCORD_PUBLIC_HOST.`,
		)
	}
	if isAllInterfaces(udpBindHost) {
		adv.Notes = append(adv.Notes, fmt.Sprintf("Server bound to %q; advertising detected addresses instead.", udpBindHost))
	}
	return adv
}

// trimScheme strips a leading URL scheme (http/https/udp/tcp) and any trailing
// slash from h, leaving a bare host.
func trimScheme(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimPrefix(h, "udp://")
	h = strings.TrimPrefix(h, "tcp://")
	return strings.TrimSuffix(h, "/")
}

// stripPort removes a trailing ":port" suffix from hostWithPort, but only when
// the segment after the last colon is numeric, so bare IPv6 addresses are left
// intact.
func stripPort(hostWithPort string) string {
	if idx := strings.LastIndex(hostWithPort, ":"); idx != -1 {
		potentialPort := hostWithPort[idx+1:]
		if _, err := strconv.Atoi(potentialPort); err == nil {
			return hostWithPort[:idx]
		}
	}
	return hostWithPort
}

// isAllInterfaces reports whether h is a wildcard/all-interfaces bind address
// ("", 0.0.0.0, ::, [::], or localhost), which cannot be advertised to remote
// clients as-is.
func isAllInterfaces(h string) bool {
	h = strings.TrimSpace(strings.ToLower(h))
	return h == "" || h == "0.0.0.0" || h == "::" || h == "[::]" || h == "localhost"
}

// detectPublicIP queries external IP-echo services (ipify, icanhazip) over HTTPS
// with a 2s per-request timeout, returning the first valid IP. It makes real
// outbound network calls and returns an error if none of the endpoints are
// reachable or return a parseable IP.
func detectPublicIP(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	endpoints := []string{
		"https://api.ipify.org?format=text",
		"https://icanhazip.com",
	}

	for _, url := range endpoints {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		b := make([]byte, 64)
		n, _ := resp.Body.Read(b)
		err = resp.Body.Close()
		if err != nil {
			return "", err
		}
		ip := strings.TrimSpace(string(b[:n]))
		if ip != "" && net.ParseIP(ip) != nil {
			return ip, nil
		}
	}
	return "", errors.New("no public IP endpoint reachable")
}

// detectLANIPPreferOutbound finds the host's primary LAN IP by opening a UDP
// socket "toward" 1.1.1.1 and reading its local address. No packets are sent
// (UDP dial only selects a route), but it does require a usable network route;
// it returns an error if none exists.
func detectLANIPPreferOutbound() (string, error) {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "", err
	}
	defer func(conn net.Conn) {
		err := conn.Close()
		if err != nil {
			fmt.Println("error closing connection:", err)
		}
	}(conn)
	localAddr := conn.LocalAddr()
	udpAddr, ok := localAddr.(*net.UDPAddr)
	if !ok || udpAddr.IP == nil {
		return "", errors.New("no local UDP addr")
	}
	return udpAddr.IP.String(), nil
}

// firstPrivateIPv4 scans network interfaces (skipping down and loopback ones)
// and returns the first private-range IPv4 address found, used as a fallback
// when the outbound-route probe fails.
func firstPrivateIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		// Skip down or loopback
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip == nil || ip.To4() == nil {
				continue
			}
			if isPrivateIPv4(ip) {
				return ip.String(), nil
			}
		}
	}
	return "", errors.New("no private IPv4 found")
}

// isPrivateIPv4 reports whether ip is in an RFC 1918 private range (10/8,
// 172.16/12, or 192.168/16). Returns false for non-IPv4 addresses.
func isPrivateIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	default:
		return false
	}
}
