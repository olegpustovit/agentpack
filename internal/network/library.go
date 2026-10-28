package network

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olegpustovit/agentpack/internal/registry"
)

type SaveAgentpackInput struct {
	Directory   string `json:"directory"`
	Name        string `json:"name,omitempty"`
	AgentpackID string `json:"agentpack_id,omitempty"`
	Label       string `json:"label,omitempty"`
	Mode        string `json:"mode,omitempty"`
	AutoCapture bool   `json:"auto_capture"`
}

type SavedAgentpack struct {
	Agentpack registry.Agentpack `json:"agentpack"`
	Revision  registry.Revision  `json:"revision"`
}

type LibraryOptions struct {
	Store      *registry.Store
	Save       func(context.Context, SaveAgentpackInput) (SavedAgentpack, error)
	Preview    func(context.Context, string, string, string) (registry.DiffResult, error)
	Restore    func(context.Context, string, string, string, bool) error
	DraftState func(context.Context, registry.Draft) (registry.DraftState, error)
}

func (g *Gateway) registerLibrary(mux *http.ServeMux) {
	library := g.options.Library
	if library.Store == nil {
		mux.HandleFunc("/api/library/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "library unavailable", http.StatusServiceUnavailable)
		})
		return
	}
	mux.HandleFunc("GET /api/library/packages", func(w http.ResponseWriter, r *http.Request) {
		value, err := library.Store.ListPackages()
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("GET /api/library/agentpacks", func(w http.ResponseWriter, r *http.Request) {
		value, err := library.Store.ListAgentpacks()
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("GET /api/library/agentpacks/{id}", func(w http.ResponseWriter, r *http.Request) {
		pack, revisions, err := library.Store.Get(r.PathValue("id"))
		libraryResponse(w, struct {
			Agentpack registry.Agentpack  `json:"agentpack"`
			Revisions []registry.Revision `json:"revisions"`
		}{pack, revisions}, err)
	})
	mux.HandleFunc("POST /api/library/agentpacks", func(w http.ResponseWriter, r *http.Request) {
		var input SaveAgentpackInput
		if !readJSON(w, r, &input) {
			return
		}
		if !g.knownDirectory(input.Directory) {
			http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
			return
		}
		value, err := library.Save(r.Context(), input)
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("PATCH /api/library/agentpacks/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		value, err := library.Store.Rename(r.PathValue("id"), input.Name)
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("DELETE /api/library/agentpacks/{id}", func(w http.ResponseWriter, r *http.Request) {
		value, err := library.Store.DeleteAgentpack(r.PathValue("id"))
		libraryResponse(w, value, err)
	})
	g.registerLibraryDrafts(mux)
	mux.HandleFunc("GET /api/library/agentpacks/{id}/revisions/{revision}/restore-preview", func(w http.ResponseWriter, r *http.Request) {
		directory := r.URL.Query().Get("directory")
		if !g.knownDirectory(directory) {
			http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
			return
		}
		value, err := library.Preview(r.Context(), r.PathValue("id"), r.PathValue("revision"), directory)
		libraryResponse(w, value, err)
	})
	mux.HandleFunc("POST /api/library/agentpacks/{id}/revisions/{revision}/restore", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
			Restart   *bool  `json:"restart"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !g.knownDirectory(input.Directory) {
			http.Error(w, "choose a recent folder from this node", http.StatusBadRequest)
			return
		}
		err := library.Restore(r.Context(), r.PathValue("id"), r.PathValue("revision"), input.Directory, input.Restart == nil || *input.Restart)
		libraryResponse(w, map[string]bool{"restored": err == nil}, err)
	})
}

func libraryResponse(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		http.Error(w, "library item not found", http.StatusNotFound)
		return
	}
	respond(w, value, err)
}

func libraryPeerRoute(method, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "library" {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	if len(parts) == 3 {
		if parts[2] == "draft-state" {
			return method == "POST"
		}
		return method == "GET" && (parts[2] == "packages" || parts[2] == "agentpacks") || method == "POST" && parts[2] == "agentpacks"
	}
	if parts[2] != "agentpacks" {
		return false
	}
	if len(parts) == 4 {
		return method == "GET" || method == "PATCH" || method == "DELETE"
	}
	return len(parts) == 7 && parts[4] == "revisions" && (method == "GET" && parts[6] == "restore-preview" || method == "POST" && parts[6] == "restore")
}
