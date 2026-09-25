package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter is a fixed-window per-IP counter (blueprint §3.8: the exact
// numbers live with the routes that use them).
type limiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	counts  map[string]int
	resetAt time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{window: window, limit: limit, counts: map[string]int{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.After(l.resetAt) {
		l.counts = map[string]int{}
		l.resetAt = now.Add(l.window)
	}
	if l.counts[key] >= l.limit {
		return false
	}
	l.counts[key]++
	return true
}

// clientIP is the key every rate limit counts under.
//
// Behind the proxy the socket peer is the proxy itself, the same for every
// visitor, so keying on it gives the whole site one shared bucket: one
// anonymous client's failed logins would lock everyone out. The configured
// header carries the real visitor instead. Its value must parse as an address;
// anything else falls back to the socket rather than becoming a key the
// client chose.
func (s *Server) clientIP(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(h))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
