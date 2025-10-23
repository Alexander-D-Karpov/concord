package netinfo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Advertised struct {
	PublicHost string // user-configured domain or detected public IP (if any)
	LANHost    string // local/LAN IP fallback
	Port       int
	Source     string // "config", "env", "http", "lan"
	Notes      []string
}

func ComputeAdvertised(ctx context.Context, userConfiguredHost, udpBindHost string, port int) Advertised {
	adv := Advertised{Port: port}

	// 0) If user configured a public host (config wins)
	if h := strings.TrimSpace(userConfiguredHost); h != "" {
		adv.PublicHost = trimScheme(h)
		adv.Source = "config"
	} else if env := strings.TrimSpace(os.Getenv("CONCORD_PUBLIC_HOST")); env != "" {
		adv.PublicHost = trimScheme(env)
		adv.Source = "env"
	} else {
		// 1) Try to detect public IP via HTTP (short timeouts)
		if ip, err := detectPublicIP(ctx); err == nil && ip != "" {
			adv.PublicHost = ip
			adv.Source = "http"
		}
	}

	// 2) Determine a LAN IP fallback
	if lan, err := detectLANIPPreferOutbound(); err == nil && lan != "" {
		adv.LANHost = lan
	} else if lan, err := firstPrivateIPv4(); err == nil && lan != "" {
		adv.LANHost = lan
	} else {
		adv.LANHost = "127.0.0.1"
		adv.Notes = append(adv.Notes, "Could not find a LAN IP; falling back to 127.0.0.1.")
	}

	// 3) Notes / hints
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

func trimScheme(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.TrimSuffix(h, "/")
}

func isAllInterfaces(h string) bool {
	h = strings.TrimSpace(strings.ToLower(h))
	return h == "" || h == "0.0.0.0" || h == "::" || h == "[::]" || h == "localhost"
}

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
