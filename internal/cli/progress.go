package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type progressWriter struct {
	mu sync.Mutex
	io.Writer
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.Writer.Write(data)
}

// Delaying the first line keeps fast commands silent; plain lines work in logs too.
func startProgress(writer io.Writer, operation string, interval time.Duration) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				fmt.Fprintf(writer, "agentpack: %s… (%s elapsed)\n", operation, time.Since(started).Round(time.Second))
				timer.Reset(5 * interval)
			}
		}
	}()
	return sync.OnceFunc(func() { close(stop); <-done })
}
