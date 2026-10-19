package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arjunvlama/toli/internal/store"
)

type startSessionIn struct {
	Title string `json:"title" jsonschema:"what this piece of work is, e.g. users api"`
}

type startSessionOut struct {
	SessionID string `json:"session_id" jsonschema:"pass to delegate to group tasks"`
	Title     string `json:"title"`
}

type deleteSessionIn struct {
	SessionID string `json:"session_id"`
}

type deleteSessionOut struct {
	Deleted bool `json:"deleted"`
}

// startSession: creates a session to group related tasks.
func (s *Server) startSession(ctx context.Context, req *sdkmcp.CallToolRequest, in startSessionIn) (*sdkmcp.CallToolResult, startSessionOut, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, startSessionOut{}, errors.New("title is required")
	}
	sess, err := s.backend.StartSession(ctx, agentName(req), title)
	if err != nil {
		return nil, startSessionOut{}, err
	}
	return nil, startSessionOut{SessionID: sess.ID, Title: sess.Title}, nil
}

// deleteSession: removes the session and its tasks; running ones are
// canceled on their peers in the background.
func (s *Server) deleteSession(ctx context.Context, req *sdkmcp.CallToolRequest, in deleteSessionIn) (*sdkmcp.CallToolResult, deleteSessionOut, error) {
	err := s.backend.DeleteSession(ctx, agentName(req), in.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, deleteSessionOut{}, fmt.Errorf("session %s not found", in.SessionID)
	}
	if err != nil {
		return nil, deleteSessionOut{}, err
	}
	return nil, deleteSessionOut{Deleted: true}, nil
}
