package obs

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var processRecording atomic.Pointer[recording]

// SetDefault binds process-owned background work; request events keep their context.
func SetDefault(sink Sink, metadata Metadata) func() {
	previous := processRecording.Swap(&recording{sink: sink, metadata: metadata})
	return func() { processRecording.Store(previous) }
}

func ProcessTrace(ctx context.Context) context.Context {
	if TraceParent(ctx) != "" {
		return ctx
	}
	header := os.Getenv("AGENTPACK_TRACE")
	if len(header) == 32 {
		trace := NewTrace()
		trace.ID = header
		if validTrace(trace) {
			header = trace.TraceParent()
		}
	}
	return WithTrace(ctx, header)
}

func Go(name string, run func()) {
	configured := processRecording.Load()
	go func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			ctx := context.Background()
			if configured != nil {
				ctx = WithSink(ctx, configured.sink, configured.metadata)
			}
			_, event := Start(ctx, "panic", name)
			switch value := value.(type) {
			case error:
				event.Fail(fmt.Errorf("panic: %w", value))
			case string:
				event.Fail(fmt.Errorf("panic: %s", value))
			default:
				event.Fail(fmt.Errorf("panic value of type %T", value))
			}
			event.End()
			if configured != nil {
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch sink := configured.sink.(type) {
					case *Store:
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						_ = sink.Flush(ctx)
					case *Spool:
						_ = sink.Close()
					}
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}
			panic(value)
		}()
		run()
	}()
}
