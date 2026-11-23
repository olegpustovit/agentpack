package gateway

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func archiveUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "archive unavailable", http.StatusServiceUnavailable)
}

func (g *Gateway) serveArchivedConversation(w http.ResponseWriter, r *http.Request, info session.SessionInfo, targetFor func(context.Context, string) (*url.URL, error)) {
	roles, err := g.store.Main(r.Context())
	if err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	w.Header().Set("X-Agentpack-Archive-Main", roles.MainName)
	if roles.MainNode == "" || roles.MainNode != roles.ArchiveNode {
		archiveUnavailable(w, r, errors.New("archive handover is not ready"))
		return
	}
	local, err := g.store.NodeID(r.Context())
	if err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	if local != roles.MainNode {
		if !g.peer(roles.MainNode).Mesh || r.Header.Get("X-Agentpack-Archive-Proxy") != "" {
			archiveUnavailable(w, r, errors.New("archive main is unavailable or changed during the read"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		target, err := targetFor(ctx, roles.MainNode)
		if err != nil {
			archiveUnavailable(w, r, err)
			return
		}
		endpoint := target.JoinPath("/api/sessions/" + info.ID + "/conversation")
		query := r.URL.Query()
		query.Del("node")
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			archiveUnavailable(w, r, err)
			return
		}
		request.Header.Set("Accept", r.Header.Get("Accept"))
		request.Header.Set("traceparent", obs.TraceParent(r.Context()))
		request.Header.Set("X-Agentpack-Archive-Proxy", "1")
		client := &http.Client{Transport: mesh.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			archiveUnavailable(w, r, errors.New("archive main is unreachable"))
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			archiveUnavailable(w, r, errors.New("archive main could not read the conversation"))
			return
		}
		for _, name := range []string{"Content-Type", "Cache-Control", "X-Agentpack-Truncated"} {
			if value := response.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		if _, err := io.Copy(w, response.Body); err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
		return
	}
	if err := g.drainArchiveEdits(r.Context(), g.tailnetTarget); err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	archive, err := state.ReadArchive(g.options.Home)
	if err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	var late bool
	if err := g.store.Reader.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM conversation_events WHERE session_id=?)", info.ID).Scan(&late); err != nil {
		archive.Close()
		archiveUnavailable(w, r, err)
		return
	}
	if late {
		archive.Close()
		// A worker can flush before the archive marker reaches its node; keep those events too.
		archive, err = state.OpenArchive(g.options.Home)
		if err == nil {
			if err = g.prepareArchiveSession(r.Context(), info.ID, g.tailnetTarget); err == nil {
				_, err = g.archiveSession(r.Context(), archive, info.ID, time.Now())
			}
		}
		if err != nil {
			if archive != nil {
				archive.Close()
			}
			archiveUnavailable(w, r, err)
			return
		}
	}
	defer archive.Close()
	var lastSeq int64
	var truncated bool
	if err := g.store.Reader.QueryRowContext(r.Context(), "SELECT last_seq,conversation_truncated FROM sessions WHERE id=?", info.ID).Scan(&lastSeq, &truncated); err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	tx, err := archive.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	defer tx.Rollback()
	var highest int64
	if err := tx.QueryRowContext(r.Context(), "SELECT coalesce(max(seq),0) FROM conversation_events WHERE session_id=?", info.ID).Scan(&highest); err != nil || highest == 0 || highest != lastSeq {
		archiveUnavailable(w, r, errors.Join(err, errors.New("archived conversation is incomplete")))
		return
	}
	entries, truncated, err := network.ReadConversationEntries(r.Context(), tx, info.ID, network.ConversationPage(r), lastSeq, truncated)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		archiveUnavailable(w, r, err)
		return
	}
	network.WriteConversationEntries(w, r, entries, truncated)
}
