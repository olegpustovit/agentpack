package network

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/olegpustovit/agentpack/internal/registry"
)

func (g *Gateway) registerLibraryDrafts(mux *http.ServeMux) {
	store := g.options.Library.Store
	mux.HandleFunc("GET /api/library/drafts", func(w http.ResponseWriter, r *http.Request) {
		value, err := store.ListDrafts()
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("POST /api/library/drafts", func(w http.ResponseWriter, r *http.Request) {
		var input registry.DraftInput
		if !readJSON(w, r, &input) {
			return
		}
		value, err := store.SaveDraft(input)
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("DELETE /api/library/drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := store.DeleteDraft(r.PathValue("id"))
		libraryResponse(w, map[string]bool{"discarded": err == nil}, err)
	})
	mux.HandleFunc("POST /api/library/drafts/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		draft, err := store.GetDraft(r.PathValue("id"))
		if err != nil {
			libraryResponse(w, nil, err)
			return
		}
		state, stateErr := g.currentDraftState(r.Context(), draft)
		value, err := store.RevalidateDraft(draft.ID, state)
		if stateErr != nil {
			value.Ready = false
			value.Reason = stateErr.Error()
		}
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("POST /api/library/draft-state", func(w http.ResponseWriter, r *http.Request) {
		var draft registry.Draft
		if !readJSON(w, r, &draft) {
			return
		}
		value, err := g.localDraftState(r.Context(), draft)
		libraryResponse(w, value, err)
	})
}

func (g *Gateway) localDraftState(ctx context.Context, draft registry.Draft) (registry.DraftState, error) {
	state := registry.DraftState{Reachable: true}
	if !g.knownDirectory(draft.Directory) {
		return state, nil
	}
	identity, err := FolderIdentity(draft.Directory)
	if err != nil {
		return state, err
	}
	state.FolderIdentity = identity
	if identity != draft.FolderIdentity {
		return state, nil
	}
	if g.options.Library.DraftState == nil {
		return state, errors.New("draft review is unavailable on this node")
	}
	state, err = g.options.Library.DraftState(ctx, draft)
	state.Reachable, state.FolderIdentity = true, identity
	return state, err
}

func (g *Gateway) currentDraftState(ctx context.Context, draft registry.Draft) (registry.DraftState, error) {
	if draft.Node == "local" {
		return g.localDraftState(ctx, draft)
	}
	target, err := tailnetTarget(ctx, g.options.Home, draft.Node)
	if err != nil {
		return registry.DraftState{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var state registry.DraftState
	err = registryPeerRequest(ctx, tailnetHTTP, target, "POST", "/api/library/draft-state", draft, &state)
	return state, err
}
