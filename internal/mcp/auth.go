package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// verifyToken maps a session's bearer token to the local agent it acts as.
// It plugs into auth.RequireBearerToken, which rejects the request (401) on
// error and otherwise hands the TokenInfo to every tool call.
func (s *Server) verifyToken(ctx context.Context, tok string, req *http.Request) (*auth.TokenInfo, error) {
	a, err := s.backend.AgentByToken(ctx, tok)
	if errors.Is(err, store.ErrNotFound) {
		return nil, auth.ErrInvalidToken
	}
	if errors.Is(err, backend.ErrAgentDisabled) {
		// Rejected like a bad token (401), but saying why.
		return nil, fmt.Errorf("%v; re-enable it in toli: %w", err, auth.ErrInvalidToken)
	}
	if err != nil {
		return nil, err
	}
	return &auth.TokenInfo{UserID: a.Name}, nil
}

// tokenFromPath lets clients pass their token in the URL (/mcp/<token>)
// instead of an Authorization header, since not every MCP client supports
// custom headers. It moves the token into the header and rewrites the path to
// /mcp, so everything after it (RequireBearerToken, verifyToken) is unchanged.
// Never log full request paths: they contain tokens.
func tokenFromPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(r.URL.Path, "/mcp/"); ok && tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
			r.URL.Path = "/mcp"
		}
		next.ServeHTTP(w, r)
	})
}

// agentName returns the local agent a tool call is acting as.
func agentName(req *sdkmcp.CallToolRequest) string {
	return req.Extra.TokenInfo.UserID
}
