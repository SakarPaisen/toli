package hook

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/backend/wake"
	"github.com/arjunvlama/toli/internal/store"
)

// SessionStart is what a session-start hook sends.
type SessionStart struct {
	Harness   string            `json:"harness"`
	SessionID string            `json:"session_id"`
	Address   map[string]string `json:"address,omitempty"` // how to reach the session; depends on the harness
}

// SessionStartResponse carries the agent's pending work, for the session's
// context ("" if there's none).
type SessionStartResponse struct {
	Context string `json:"context"`
}

// AgentResponse names the agent a token belongs to.
type AgentResponse struct {
	Agent string `json:"agent"`
}

// NotesResponse is the notifications waiting for a session (bridge
// harnesses' plugins collect them).
type NotesResponse struct {
	Notes []string `json:"notifications"`
}

// notesWait is how long GET /notifications holds the request when nothing's waiting.
var notesWait = 30 * time.Second

// SessionEnd is what a session-end hook sends.
type SessionEnd struct {
	SessionID string `json:"session_id"`
}

// Handler serves the hook endpoints. It goes on the MCP listener, and takes
// the agent's token as MCP does: in the path (/hook/<token>/notifications) or as
// "Authorization: Bearer <token>" (/hook/notifications).
func Handler(b *backend.Backend) http.Handler {
	mux := http.NewServeMux()
	handle := func(route string, f http.HandlerFunc) {
		method, name, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /hook/{token}/"+name, f)
		mux.HandleFunc(method+" /hook/"+name, f)
	}
	handle("GET agent", func(w http.ResponseWriter, r *http.Request) {
		agent, ok := authenticate(b, w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, AgentResponse{Agent: agent})
	})
	handle("POST session-start", func(w http.ResponseWriter, r *http.Request) {
		agent, ok := authenticate(b, w, r)
		if !ok {
			return
		}
		var req SessionStart
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Harness == "" || req.SessionID == "" {
			writeError(w, http.StatusBadRequest, "harness and session_id are required")
			return
		}
		// It may go on a command line (codex queue).
		if strings.Trim(req.SessionID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:") != "" {
			writeError(w, http.StatusBadRequest, "session_id may only have letters, digits, -, _, . and :")
			return
		}
		pending, err := b.RegisterHarnessSession(r.Context(), agent, req.Harness, req.SessionID, req.Address)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, SessionStartResponse{Context: pending})
	})
	handle("GET notifications", func(w http.ResponseWriter, r *http.Request) {
		agent, ok := authenticate(b, w, r)
		if !ok {
			return
		}
		session := r.URL.Query().Get("session")
		if session == "" {
			writeError(w, http.StatusBadRequest, "session is required")
			return
		}
		notes, err := b.TakeNotes(r.Context(), agent, session, notesWait)
		switch {
		case errors.Is(err, wake.ErrSessionReplaced):
			writeError(w, http.StatusGone, err.Error()) // the plugin stops polling
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, NotesResponse{Notes: append([]string{}, notes...)})
	})
	handle("POST session-end", func(w http.ResponseWriter, r *http.Request) {
		agent, ok := authenticate(b, w, r)
		if !ok {
			return
		}
		var req SessionEnd
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := b.UnregisterHarnessSession(r.Context(), agent, req.SessionID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// authenticate returns the agent whose token is in the path or the
// Authorization header, or writes a 401.
func authenticate(b *backend.Backend, w http.ResponseWriter, r *http.Request) (string, bool) {
	tok := r.PathValue("token")
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	a, err := b.AgentByToken(r.Context(), tok)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "unknown token")
		return "", false
	case errors.Is(err, backend.ErrAgentDisabled):
		writeError(w, http.StatusUnauthorized, err.Error())
		return "", false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	return a.Name, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("wake: write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
