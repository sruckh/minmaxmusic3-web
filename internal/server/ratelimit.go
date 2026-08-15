package server

import (
	"net"
	"net/http"
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

func clientIP(r *http.Request) string {
	// Behind NPM the socket peer is the proxy; trust nothing else.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
