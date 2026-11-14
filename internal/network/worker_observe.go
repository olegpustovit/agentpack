package network

import (
	"sync"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// diagnosticStderr retains only the adapter's separate diagnostic stream.
type diagnosticStderr struct {
	mu   sync.Mutex
	data []byte
}

func (tail *diagnosticStderr) Write(data []byte) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	n := len(data)
	if n >= 8<<10 {
		tail.data = append(tail.data[:0], data[n-(8<<10):]...)
	} else {
		if extra := len(tail.data) + n - (8 << 10); extra > 0 {
			copy(tail.data, tail.data[extra:])
			tail.data = tail.data[:len(tail.data)-extra]
		}
		tail.data = append(tail.data, data...)
	}
	return n, nil
}

func (tail *diagnosticStderr) String() string {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	return string(tail.data)
}

func (tail *diagnosticStderr) Redacted() any {
	text := tail.String()
	if obs.CredentialShaped(text) {
		return "[redacted]"
	}
	// Individual attributes cap strings at 4 KiB; retain the 8 KiB tail in two chunks.
	parts := []string{text[:min(len(text), 4096)]}
	if len(text) > 4096 {
		parts = append(parts, text[4096:])
	}
	return obs.Redact(parts)
}
