package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/registry"
)

const draftReviewLimit = 64 << 20

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
	if !sameFolder(draft.FolderIdentity, identity) {
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
	target, err := g.tailnetTarget(ctx, draft.Node)
	if err != nil {
		return registry.DraftState{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var state registry.DraftState
	body, err := json.Marshal(draft)
	if err != nil {
		return state, err
	}
	if len(body) > draftReviewLimit {
		return state, errors.New("draft review request is too large")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.JoinPath("/api/library/draft-state").String(), bytes.NewReader(body))
	if err != nil {
		return state, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", obs.TraceParent(ctx))
	response, err := tailnetHTTP.Do(request)
	if err != nil {
		return state, errors.New("draft machine is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return state, errors.New("draft machine could not review this draft")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, draftReviewLimit+1))
	if err != nil {
		return state, err
	}
	if len(data) > draftReviewLimit {
		return state, errors.New("draft review response is too large")
	}
	err = json.Unmarshal(data, &state)
	return state, err
}
