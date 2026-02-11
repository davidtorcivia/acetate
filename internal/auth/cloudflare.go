package auth

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var cfIPURLs = []string{
	"https://www.cloudflare.com/ips-v4/",
	"https://www.cloudflare.com/ips-v6/",
}

// CloudflareIPs holds the known Cloudflare IP ranges for trusted header extraction.
type CloudflareIPs struct {
	mu      sync.RWMutex
	nets    []*net.IPNet
	done    chan struct{}
}

// NewCloudflareIPs fetches Cloudflare IP ranges and starts a refresh goroutine.
func NewCloudflareIPs() *CloudflareIPs {
	cf := &CloudflareIPs{done: make(chan struct{})}
	cf.refresh()
	go cf.refreshLoop()
	return cf
}

// Close stops the refresh goroutine.
func (cf *CloudflareIPs) Close() {
	close(cf.done)
}

// IsTrusted checks if the given IP is a known Cloudflare IP.
func (cf *CloudflareIPs) IsTrusted(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	cf.mu.RLock()
	defer cf.mu.RUnlock()

	for _, n := range cf.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// GetClientIP extracts the real client IP from a request.
// If the request comes from a trusted Cloudflare IP, use CF-Connecting-IP.
// Otherwise, fall back to RemoteAddr.
func (cf *CloudflareIPs) GetClientIP(r *http.Request) string {
	remoteIP := extractIP(r.RemoteAddr)

	if cf.IsTrusted(remoteIP) {
		if cfIP := r.Header.Get("CF-Connecting-IP"); cfIP != "" {
			return cfIP
		}
	}

	return remoteIP
}

func extractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func (cf *CloudflareIPs) refresh() {
	var nets []*net.IPNet

	client := &http.Client{Timeout: 10 * time.Second}

	for _, url := range cfIPURLs {
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("cloudflare: failed to fetch %s: %v", url, err)
			continue
		}
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			_, cidr, err := net.ParseCIDR(line)
			if err != nil {
				continue
			}
			nets = append(nets, cidr)
		}
		resp.Body.Close()
	}

	if len(nets) > 0 {
		cf.mu.Lock()
		cf.nets = nets
		cf.mu.Unlock()
		log.Printf("cloudflare: loaded %d IP ranges", len(nets))
	}
}

func (cf *CloudflareIPs) refreshLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cf.refresh()
		case <-cf.done:
			return
		}
	}
}
