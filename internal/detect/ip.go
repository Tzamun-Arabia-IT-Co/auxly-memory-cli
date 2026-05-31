package detect

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// LocalIP returns the active non-loopback local IPv4 address.
func LocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// PublicIP retrieves the public WAN IP of the machine with a strict 1-second timeout.
func PublicIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	if err != nil {
		return "(offline / private network)"
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "(offline / private network)"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "(offline / private network)"
	}

	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "(offline / private network)"
	}
	return ip
}

// GetEnvironmentType returns the execution environment context (e.g. Docker, WSL, Remote-SSH, Local).
func GetEnvironmentType() string {
	// Check WSL
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_ENV") != "" {
		return "WSL Distro (Linux)"
	}

	// Check Docker/Container
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "Docker Container (Linux)"
	}

	// Check Remote SSH
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" {
		return "Remote-SSH Session"
	}

	// Check devcontainers / codespaces
	if os.Getenv("REMOTE_CONTAINERS") == "true" || os.Getenv("CODESPACES") == "true" {
		return "DevContainer Session"
	}

	return "Local Host"
}
