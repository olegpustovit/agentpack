package gateway

import (
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
)

type loginFailure struct {
	count       int
	last, until time.Time
}

// limiterKey is who a request counts against: an IPv6 client by its /64, which one host can rotate
// through freely. local is this machine's own loopback origin, which the internet cannot exhaust.
func limiterKey(r *http.Request) (key string, local bool) {
	address, err := netip.ParseAddr(sourceOf(r).client)
	if err != nil {
		return sourceOf(r).client, false
	}
	address = address.Unmap()
	if address.Is6() {
		prefix, _ := address.Prefix(64)
		return prefix.String(), false
	}
	return address.String(), address.IsLoopback()
}

func loginKeys(r *http.Request, email string) [2]string {
	key, _ := limiterKey(r)
	return [2]string{"email:" + email, "address:" + key}
}

func (a *gatewayAuth) allowLogin(w http.ResponseWriter, r *http.Request, email string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	client, local := limiterKey(r)
	wait := a.attempts.Take(now, client, 10, network.LimiterCeiling(local, 100))
	for _, key := range loginKeys(r, email) {
		// Failed internet logins must not lock the owner out of this machine's own UI.
		if local && strings.HasPrefix(key, "email:") {
			continue
		}
		if remaining := a.failures[key].until.Sub(now); remaining > wait {
			wait = remaining
		}
	}
	if wait > 0 {
		network.RetryAfter(w, wait)
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
