package state

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkChangesFirstSync pages a fresh peer through every event; the cost per event must stay flat as history grows.
func BenchmarkChangesFirstSync(b *testing.B) {
	for _, events := range []int{20_000, 200_000} {
		b.Run(fmt.Sprint(events), func(b *testing.B) {
			s, err := Open(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			tx, err := s.DB.Begin()
			if err != nil {
				b.Fatal(err)
			}
			body := strings.Repeat("x", 200)
			for i := range events {
				// One version per 100 events, like a sink batch.
				if i%100 == 0 && i > 0 {
					if err := tx.Commit(); err != nil {
						b.Fatal(err)
					}
					if tx, err = s.DB.Begin(); err != nil {
						b.Fatal(err)
					}
				}
				if _, err := tx.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES ('s',?,?)", i, body); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				since, pages := int64(0), 0
				for {
					_, next, err := s.Replicator().Changes(b.Context(), since, nil, 4<<20)
					if err != nil {
						b.Fatal(err)
					}
					if next == since {
						break
					}
					since = next
					pages++
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*events), "ns/event")
			}
		})
	}
}
