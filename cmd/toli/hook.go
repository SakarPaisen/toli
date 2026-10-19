package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/arjunvlama/toli/internal/hook"
)

// runHook is `toli hook <harness> <event> <MCP URL>`, run by a coding agent's
// hooks. A failure exits 1, which for session start and end is a
// non-blocking error: the session goes on, and the harness shows why.
func runHook(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: toli hook <harness> <%s|%s> <MCP URL>", hook.EventSessionStart, hook.EventSessionEnd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hook.Run(ctx, hook.Call{
		Harness: args[0], Event: args[1], MCPURL: args[2],
		Stdin: os.Stdin, Stdout: os.Stdout, Getenv: os.Getenv,
	})
}
