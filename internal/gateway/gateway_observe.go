package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/felixge/httpsnoop"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

type httpObservation struct {
	code       atomic.Int32
	bytes      atomic.Int64
	connection net.Conn
}

func (o *httpObservation) writer(w http.ResponseWriter) http.ResponseWriter {
	return httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if code >= 200 || code == 101 {
					o.code.CompareAndSwap(0, int32(code))
				}
				next(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(p []byte) (int, error) {
				o.code.CompareAndSwap(0, 200)
				n, err := next(p)
				o.bytes.Add(int64(n))
				return n, err
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(reader io.Reader) (int64, error) {
				o.code.CompareAndSwap(0, 200)
				n, err := next(reader)
				o.bytes.Add(n)
				return n, err
			}
		},
		Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
			return func() { o.code.CompareAndSwap(0, 200); next() }
		},
		Hijack: func(next httpsnoop.HijackFunc) httpsnoop.HijackFunc {
			return func() (net.Conn, *bufio.ReadWriter, error) {
				conn, rw, err := next()
				if err == nil {
					o.code.CompareAndSwap(0, 101)
					o.connection = conn
				}
				return conn, rw, err
			}
		},
	})
}

func observeHTTP(mux *http.ServeMux, next http.Handler, sink obs.Sink, metadata obs.Metadata) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			trace := obs.ParseTraceParent(r.Header.Get("traceparent"))
			w.Header().Set("X-Agentpack-Trace", trace.ID)
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.Handler(r)
		if pattern == "" {
			pattern = "unmatched"
		}
		ctx := obs.WithTrace(obs.WithSink(r.Context(), sink, metadata), r.Header.Get("traceparent"))
		ctx, event := obs.Start(ctx, "http", pattern)
		r = r.WithContext(ctx)
		w.Header().Set("X-Agentpack-Trace", event.TraceID)
		if actor, ok := session.ActorFrom(ctx); ok {
			event.Set("user", actor.User)
		}
		event.Set("node", r.URL.Query().Get("node"))
		if strings.Contains(pattern, "/api/sessions/{id}") {
			parts := strings.SplitN(r.URL.EscapedPath(), "/", 5)
			if len(parts) >= 4 {
				if id, err := url.PathUnescape(parts[3]); err == nil && network.ValidPeerSessionID(id) {
					event.Set("session_id", id)
				}
			}
		}
		metrics := &httpObservation{}
		observed := metrics.writer(w)
		defer event.End()
		defer func() {
			problem := recover()
			abort := false
			if problem == http.ErrAbortHandler {
				// The reverse proxy's way to drop a response whose client or upstream went away: not a crash.
				event.Status = int(metrics.code.Load())
				event.Set("aborted", true)
				event.Set("response_bytes", metrics.bytes.Load())
				panic(problem)
			}
			if problem != nil {
				event.Kind = "panic"
				switch value := problem.(type) {
				case error:
					event.Fail(fmt.Errorf("panic: %w", value))
				case string:
					event.Fail(fmt.Errorf("panic: %s", value))
				default:
					event.Fail(fmt.Errorf("panic value of type %T", value))
				}
				event.Status = http.StatusInternalServerError
				if metrics.connection != nil {
					_ = metrics.connection.Close()
				} else if metrics.code.Load() == 0 {
					http.Error(observed, "internal server error", http.StatusInternalServerError)
				} else {
					abort = true
				}
			} else {
				event.Status = int(metrics.code.Load())
				if event.Status == 0 {
					event.Status = http.StatusOK
				}
				if event.Status >= 500 && event.Level != "error" {
					event.Fail(fmt.Errorf("HTTP %d", event.Status))
				}
			}
			if r.Pattern != "" {
				event.Name = r.Pattern
			}
			event.Set("response_bytes", metrics.bytes.Load())
			if abort {
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(observed, r)
	})
}
