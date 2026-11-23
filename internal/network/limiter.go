package network

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// ClientWindow counts requests per client address in one-minute windows. The ceiling across all
// clients bounds memory and work when many addresses take part.
type ClientWindow struct {
	reset  time.Time
	total  int
	counts map[string]int
}

// Take admits one request from client, or returns how long to wait. The caller holds its lock.
func (c *ClientWindow) Take(now time.Time, client string, perClient, ceiling int) time.Duration {
	if !now.Before(c.reset) {
		c.reset, c.total, c.counts = now.Add(time.Minute), 0, map[string]int{}
	}
	if c.counts[client] >= perClient || c.total >= ceiling {
		return c.reset.Sub(now)
	}
	c.counts[client]++
	c.total++
	return 0
}

// LimiterCeiling is the cap across all clients, which the local origin never shares.
func LimiterCeiling(local bool, ceiling int) int {
	if local {
		return math.MaxInt
	}
	return ceiling
}

// RetryAfter tells a limited client how many whole seconds to wait.
func RetryAfter(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int((wait+time.Second-1)/time.Second))))
}
