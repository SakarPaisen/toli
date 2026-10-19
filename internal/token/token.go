// Package token makes and hashes agent tokens. A token is the link between a
// coding-agent session (via its MCP config) and a local agent identity. Only
// the hash is stored.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// prefix makes leaked tokens recognizable to people and secret scanners.
const prefix = "toli_"

// New returns a fresh random token and its hash.
func New() (tok, hash string) {
	tok = prefix + rand.Text()
	return tok, Hash(tok)
}

// Hash is what gets stored and looked up; the raw token is never stored.
// Tokens are high-entropy, so a fast unsalted hash is safe and keeps lookups
// by hash possible.
func Hash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
