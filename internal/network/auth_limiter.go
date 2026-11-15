package network

import (
	"net"
	"net/http"
	"strconv"
	"time"
)

type loginFailure struct {
	count       int
	last, until time.Time
}

func loginKeys(r *http.Request, email string) [2]string {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	return [2]string{"email:" + email, "address:" + address}
}

func (a *gatewayAuth) allowLogin(w http.ResponseWriter, r *http.Request, email string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if !now.Before(a.reset) {
		a.attempts, a.reset = 0, now.Add(time.Minute)
	}
	wait := time.Duration(0)
	if a.attempts >= 10 {
		wait = a.reset.Sub(now)
	}
	for _, key := range loginKeys(r, email) {
		if remaining := a.failures[key].until.Sub(now); remaining > wait {
			wait = remaining
		}
	}
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((wait+time.Second-1)/time.Second))))
		http.Error(w, "too many attempts; retry later", http.StatusTooManyRequests)
		return false
	}
	a.attempts++
	return true
}

func (a *gatewayAuth) failedLogin(r *http.Request, email string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	// The global ten-per-minute gate bounds these thirty-minute records.
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
