package mcp

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arjunvlama/toli/internal/store"
)

// peer is the slice of a peer's Agent Card the coding agent needs: what to
// call it and what it can do (skills as A2A defines them). Reaching it
// (interfaces, auth, capabilities) is the daemon's job, so those fields stay
// out.
type peer struct {
	Peer        string           `json:"peer" jsonschema:"pass this to delegate"`
	Name        string           `json:"name" jsonschema:"what the peer calls itself"`
	Description string           `json:"description,omitempty"`
	Skills      []a2a.AgentSkill `json:"skills,omitempty"`
	Reachable   *bool            `json:"reachable,omitempty" jsonschema:"false if the daemon's last check of this peer failed"`
}

type listPeersOut struct {
	Peers []peer `json:"peers"`
}

// toPeer trims a stored peer down to what the coding agent sees.
func toPeer(p store.Peer) peer {
	if p.Card == nil { // card not fetched yet
		return peer{Peer: p.Name, Name: p.Name}
	}
	return peer{
		Peer:        p.Name,
		Name:        p.Card.Name,
		Description: p.Card.Description,
		Skills:      p.Card.Skills,
	}
}

func (s *Server) listPeers(ctx context.Context, req *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, listPeersOut, error) {
	peers, err := s.backend.ListPeers(ctx, agentName(req))
	if err != nil {
		return nil, listPeersOut{}, err
	}

	out := listPeersOut{Peers: make([]peer, 0, len(peers))}
	for _, p := range peers {
		v := toPeer(p)
		if st, err := s.backend.PeerStatus(p.ID); err == nil { // no status (no client): leave it out
			if !st.LastSeen.IsZero() || st.LastError != nil { // checked
				reachable := st.LastError == nil
				v.Reachable = &reachable
			}
		}
		out.Peers = append(out.Peers, v)
	}
	return nil, out, nil
}
