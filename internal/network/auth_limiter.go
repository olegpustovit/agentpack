package network

import (
	"net/http"
	"strconv"
	"time"
)

type loginFailure struct {
	count       int
	last, until time.Time
}

// clientWindow counts requests per client address in one-minute windows. The ceiling across all
// clients bounds memory and work when many addresses take part.
type clientWindow struct {
	reset  time.Time
	total  int
	counts map[string]int
}

// take admits one request from client, or returns how long to wait. The caller holds its lock.
func (c *clientWindow) take(now time.Time, client string, perClient, ceiling int) time.Duration {
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

func loginKeys(r *http.Request, email string) [2]string {
	return [2]string{"email:" + email, "address:" + sourceOf(r).client}
}

func retryAfter(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int((wait+time.Second-1)/time.Second))))
}

func (a *gatewayAuth) allowLogin(w http.ResponseWriter, r *http.Request, email string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	wait := a.attempts.take(now, sourceOf(r).client, 10, 100)
	for _, key := range loginKeys(r, email) {
		if remaining := a.failures[key].until.Sub(now); remaining > wait {
			wait = remaining
		}
	}
	if wait > 0 {
		retryAfter(w, wait)
		http.Error(w, "too many attempts; retry later", http.StatusTooManyRequests)
		return false
	}
	return true
}

func (a *gatewayAuth) failedLogin(r *http.Request, email string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	// The hundred-per-minute ceiling bounds these thirty-minute records.
	for key, failure := range a.failures {
		if now.Sub(failure.last) > 30*time.Minute {
			delete(a.failures, key)
		}
	}
	for _, key := range loginKeys(r, email) {
		failure := a.failures[key]
		failure.count = min(failure.count+1, 15)
		failure.last = now
		if failure.count >= 5 {
			failure.until = now.Add(min(time.Second<<(failure.count-5), 15*time.Minute))
		}
		a.failures[key] = failure
	}
}
