package network

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/state"
)

const syncPageLimit = 4 << 20

// The local quarantine migration leaves the replicated schema unchanged.
const replicationVersion = 44

type stateChangePage struct {
	reachable bool
	Schema    int            `json:"schema"`
	SiteID    []byte         `json:"site_id"`
	Next      int64          `json:"next"`
	Changes   []state.Change `json:"changes"`
	Transfer  *stateTransfer `json:"transfer,omitempty"`
	Data      []byte         `json:"data,omitempty"`
	Offset    int64          `json:"offset,omitempty"`
}

// peerOnly requires the identity attached by peerHandler, never a caller's header.
func (g *Gateway) peerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, _ := r.Context().Value(replicaPeerKey{}).(string)
		if !g.enrolledPeer(peer) {
			http.Error(w, "replication requires enrollment on both machines", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (g *Gateway) meshPeer(ctx context.Context, id string) bool {
	if g.options.Library.Store == nil || id == "" {
		return false
	}
	peers, err := g.options.Library.Store.ListPeers()
	if err != nil {
		return false
	}
	for _, peer := range peers {
		if peer.Node == id {
			return peer.Mesh
		}
	}
	return false
}

func (g *Gateway) meshOwner(w http.ResponseWriter, r *http.Request) bool {
	actor, ok := actorFrom(r.Context())
	if ok && actor.Kind == "local" {
		return true
	}
	var owner bool
	if ok && (actor.Kind == "browser" || actor.Kind == "mcp") {
		err := g.auth.store.Reader.QueryRowContext(r.Context(), "SELECT role='owner' AND disabled=0 FROM users WHERE email=?", actor.User).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
	}
	if !owner {
		http.Error(w, "mesh enrollment requires an owner", http.StatusForbidden)
	}
	return owner
}

func (g *Gateway) registerStateSync(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sync/events", g.peerOnly(func(w http.ResponseWriter, r *http.Request) {
		peer, _ := r.Context().Value(replicaPeerKey{}).(string)
		if !g.meshPeer(r.Context(), peer) {
			http.Error(w, "mesh replication requires explicit enrollment on both machines", http.StatusForbidden)
			return
		}
		g.serveEvents(w, r)
	}))
	mux.HandleFunc("GET /api/sync/changes", g.peerOnly(func(w http.ResponseWriter, r *http.Request) {
		peer, _ := r.Context().Value(replicaPeerKey{}).(string)
		if !g.meshPeer(r.Context(), peer) {
			http.Error(w, "mesh replication requires explicit enrollment on both machines", http.StatusForbidden)
			return
		}
		schema, err := strconv.Atoi(r.URL.Query().Get("schema"))
		if err != nil {
			http.Error(w, "invalid schema version", http.StatusBadRequest)
			return
		}
		if schema != replicationVersion {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, struct {
				Schema     int `json:"schema"`
				PeerSchema int `json:"peer_schema"`
			}{replicationVersion, schema})
			return
		}
		if r.URL.Query().Has("transfer") {
			g.serveStateTransfer(w, r, peer)
			return
		}
		sinceText := r.URL.Query().Get("since")
		since := int64(0)
		if sinceText != "" {
			since, err = strconv.ParseInt(sinceText, 10, 64)
		}
		if err != nil || since < 0 {
			http.Error(w, "invalid sync cursor", http.StatusBadRequest)
			return
		}
		var exclude []byte
		if raw := r.URL.Query().Get("exclude"); raw != "" {
			exclude, err = hex.DecodeString(raw)
			if err != nil || len(exclude) != 16 {
				http.Error(w, "invalid excluded site", http.StatusBadRequest)
				return
			}
		}
		site, err := g.auth.store.SiteID(r.Context())
		var rows []state.Change
		var next int64
		// Reserve space for the envelope in the 4 MiB response limit.
		if err == nil && sinceText != "" {
			rows, next, err = g.auth.store.Changes(r.Context(), since, exclude, syncPageLimit-1024)
		}
		var oversized *state.ChangeVersionTooLargeError
		if errors.As(err, &oversized) {
			var transfer *stateTransfer
			transfer, err = g.newStateTransfer(r.Context(), peer, oversized.Version, exclude)
			if err == nil {
				writeJSON(w, stateChangePage{Schema: replicationVersion, SiteID: site, Next: oversized.Version, Transfer: transfer})
				return
			}
		}
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			status := http.StatusInternalServerError
			if errors.Is(err, state.ErrChangeVersionTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "state changes unavailable", status)
			return
		}
		writeJSON(w, stateChangePage{Schema: replicationVersion, SiteID: site, Next: next, Changes: rows})
	}))
}

var errSyncNeedsUpdate = errors.New("peer needs update")

func fetchStatePage(ctx context.Context, client *http.Client, target *url.URL, since int64, exclude []byte) (stateChangePage, error) {
	var page stateChangePage
	endpoint := target.JoinPath("/api/sync/changes")
	query := url.Values{"schema": {strconv.Itoa(replicationVersion)}, "since": {strconv.FormatInt(since, 10)}, "exclude": {hex.EncodeToString(exclude)}}
	if since < 0 {
		query.Del("since")
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return page, err
	}
	request.Header.Set("traceparent", obs.TraceParent(ctx))
	response, err := client.Do(request)
	if err != nil {
		return page, errors.New("state peer is unreachable")
	}
	defer response.Body.Close()
	page.reachable = true
	if response.StatusCode == http.StatusConflict {
		_ = json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&page)
		return page, errSyncNeedsUpdate
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden {
		return page, errSyncNeedsUpdate
	}
	if response.StatusCode != http.StatusOK {
		return page, fmt.Errorf("state peer returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, syncPageLimit+1))
	if err != nil || len(body) > syncPageLimit {
		return page, errors.New("state page is incomplete or too large")
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return page, errors.New("invalid state page")
	}
	if page.Schema != replicationVersion {
		return page, errSyncNeedsUpdate
	}
	if len(page.SiteID) != 16 || page.Next < since {
		return page, errors.New("invalid state page identity or cursor")
	}
	if len(page.Data) != 0 || page.Offset != 0 {
		return page, errors.New("unexpected state transfer data")
	}
	if page.Transfer != nil {
		if since < 0 || page.Next <= since || len(page.Changes) != 0 || len(page.Data) != 0 {
			return page, errors.New("invalid state transfer envelope")
		}
		page.Changes, err = fetchStateTransfer(ctx, client, target, page)
		if err != nil {
			return page, err
		}
	}
	return page, nil
}

// pullState resumes each source by its immutable site ID, including after gateway restarts.
func pullState(ctx context.Context, store *state.Store, library *registry.Store, client *http.Client, target *url.URL) (stateChangePage, bool, error) {
	local, err := store.SiteID(ctx)
	if err != nil {
		return stateChangePage{}, false, err
	}
	page, err := fetchStatePage(ctx, client, target, -1, local)
	if err != nil {
		return page, false, err
	}
	site := page.SiteID
	if bytes.Equal(site, local) {
		return page, false, errors.New("cannot replicate a copy of the local database")
	}
	since, err := store.Cursor(ctx, site)
	if err != nil {
		return page, false, err
	}
	changed := false
	page, err = fetchStatePage(ctx, client, target, since, local)
	if err != nil {
		return page, changed, err
	}
	for {
		if !bytes.Equal(site, page.SiteID) {
			return page, changed, errors.New("peer database identity changed during transfer")
		}
		rejected, err := store.ApplyValidatedPage(ctx, page.Changes, site, page.Next, func(change state.Change) (bool, error) {
			err := registry.ValidateChanges(store, []state.Change{change})
			if errors.Is(err, registry.ErrInvalidChange) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return page, changed, err
		}
		if len(rejected) > 0 {
			_, event := obs.Start(ctx, "state.sync", "quarantine")
			event.Set("count", len(rejected))
			event.Set("peer_site", hex.EncodeToString(site))
			event.Set("version", rejected[0].Version)
			event.Fail(errors.New(rejected[0].Reason))
			event.End()
		}
		applied := len(page.Changes) > len(rejected)
		changed = changed || applied
		if library != nil && applied {
			for _, change := range page.Changes {
				if strings.HasPrefix(change.Table, "registry_") {
					if err := library.MergeHeads(); err != nil {
						return page, changed, err
					}
					if _, err := library.CollectGarbage(); err != nil {
						return page, changed, err
					}
					break
				}
			}
		}
		if page.Next == since || len(page.Changes) == 0 {
			return page, changed, nil
		}
		since = page.Next
		page, err = fetchStatePage(ctx, client, target, since, local)
		if err != nil {
			return page, changed, err
		}
	}
}

func (g *Gateway) wakeStatePeer(peer string) {
	if value, ok := g.statePulls.Load(peer); ok {
		select {
		case value.(chan struct{}) <- struct{}{}:
		default:
		}
	}
}

// The manager owns worker lifetimes; each peer pulls independently so one offline node cannot stall the mesh.
func (g *Gateway) runStateSync(ctx context.Context) {
	if g.logs != nil {
		nodeID, _ := g.auth.store.NodeID(ctx)
		ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, nodeID, "gateway"))
	}
	if err := g.publishMeshNode(ctx); err != nil {
		_, event := obs.Start(ctx, "sync.pull", "register node")
		event.Fail(err)
		event.End()
	}
	if g.options.Library.Store == nil {
		return
	}
	type running struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	peers := map[string]running{}
	defer func() {
		for _, p := range peers {
			p.cancel()
		}
		for _, p := range peers {
			<-p.done
		}
	}()
	events, unsubscribe := g.listen()
	defer func() { unsubscribe() }()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := g.cleanStateTransfers(time.Now()); err != nil {
			_, event := obs.Start(ctx, "sync.pull", "clean expired transfers")
			event.Fail(err)
			event.End()
		}
		list, err := g.options.Library.Store.ListPeers()
		if err != nil {
			_, event := obs.Start(ctx, "sync.pull", "list peers")
			event.Fail(err)
			event.End()
		} else {
			present := map[string]bool{}
			for _, peer := range list {
				if !peer.Mesh {
					continue
				}
				present[peer.Node] = true
				if prior, ok := peers[peer.Node]; ok {
					select {
					case <-prior.done:
						delete(peers, peer.Node)
					default:
						continue
					}
				}
				peerCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				wake := make(chan struct{}, 1)
				g.statePulls.Store(peer.Node, wake)
				peers[peer.Node] = running{cancel, done}
				obs.Go("gateway.state_peer", func() {
					defer close(done)
					defer g.statePulls.Delete(peer.Node)
					g.runStatePeer(peerCtx, peer.Node, wake, 30*time.Second, g.tailnetTarget)
				})
			}
			for id, p := range peers {
				if !present[id] {
					p.cancel()
					<-p.done
					delete(peers, id)
				}
			}
		}
	waitForTick:
		for {
			select {
			case <-ctx.Done():
				return
			case message, open := <-events:
				if !open {
					unsubscribe()
					events, unsubscribe = g.listen()
				} else if message.Topic == "replication" && message.Node == "" {
					break waitForTick
				}
			case <-ticker.C:
				break waitForTick
			}
		}
	}
}

func (g *Gateway) runStatePeer(ctx context.Context, peer string, wake <-chan struct{}, interval time.Duration, targetFor func(context.Context, string) (*url.URL, error)) {
	delay := time.Second
	client := &http.Client{Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// A peer failing the same way on every retry is recorded once; each record is a replicated write.
	lastFailure := ""
	for {
		if !g.meshPeer(ctx, peer) {
			return
		}
		pullCtx, cancel := context.WithTimeout(ctx, time.Minute)
		pullCtx, event := obs.Start(pullCtx, "sync.pull", "state")
		target, err := targetFor(pullCtx, peer)
		var page stateChangePage
		var changed bool
		if err == nil {
			page, changed, err = pullState(pullCtx, g.auth.store, g.options.Library.Store, client, target)
		}
		if err == nil && changed {
			g.credentialsChanged()
		}
		if err == nil {
			archiveCtx, archiveEvent := obs.Start(pullCtx, "archive.edits", "relayed history")
			if archiveErr := g.drainArchiveEdits(archiveCtx, targetFor); archiveErr != nil {
				archiveEvent.Fail(archiveErr)
				archiveEvent.End()
			} else if changed {
				archiveEvent.End()
			}
			if cleanupErr := g.cleanupArchivedBlobs(archiveCtx, targetFor); cleanupErr != nil {
				_, cleanupEvent := obs.Start(archiveCtx, "archive.cleanup", "local attachments")
				cleanupEvent.Fail(cleanupErr)
				cleanupEvent.End()
			}
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		contact := replicaContact{Reachable: page.reachable, At: time.Now().UTC(), Schema: page.Schema, Version: page.Next}
		previous, known := g.replicaStatus.Load(peer)
		prior, _ := previous.(replicaContact)
		contact.LastSuccess = prior.LastSuccess
		if err == nil {
			contact.LastSuccess = contact.At
			zero := int64(0)
			contact.Lag = &zero
		}
		wait := interval
		failure := ""
		if err != nil {
			failure = err.Error()
			contact.Error = failure
			contact.NeedsUpdate = errors.Is(err, errSyncNeedsUpdate)
			event.Fail(err)
			wait = min(delay, interval)
			delay = min(delay*2, interval)
		} else {
			delay = time.Second
		}
		g.replicaStatus.Store(peer, contact)
		if !known || prior.Reachable != contact.Reachable || prior.Error != contact.Error ||
			prior.NeedsUpdate != contact.NeedsUpdate || prior.Schema != contact.Schema {
			g.emitEvent(eventMessage{Topic: "replication"})
		}
		if err != nil && failure != lastFailure || changed {
			event.End()
		}
		lastFailure = failure
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (g *Gateway) publishMeshNode(ctx context.Context) error {
	site, err := g.auth.store.SiteID(ctx)
	if err != nil {
		return err
	}
	_, err = g.auth.store.DB.ExecContext(ctx, `INSERT INTO mesh_nodes(node_id,name,site_id,schema_version,app_version,updated_at)
 VALUES (`+state.LocalNodeSQL+`,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET
 name=excluded.name,site_id=excluded.site_id,schema_version=excluded.schema_version,app_version=excluded.app_version,updated_at=excluded.updated_at
 WHERE name IS NOT excluded.name OR site_id IS NOT excluded.site_id OR schema_version IS NOT excluded.schema_version OR app_version IS NOT excluded.app_version`, g.selfIdentity(ctx).displayName(), site, replicationVersion, g.options.Version, time.Now().UnixMilli())
	return err
}
