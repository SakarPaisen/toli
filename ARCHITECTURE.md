# Architecture

This is a map of toli for people changing its code: what the parts are, how a task moves through them, and why they're built the way they are. For using toli, see the [README](README.md).

## Overview

```
                     ┌─────────────────── toli daemon ──────────────────┐
 agent ──────────────┼─▶ MCP server   /mcp/, /hook/ ──┐                 │
   agent token       │                                │                 │
 human (TUI, CLI) ───┼─▶ admin API    /v1/ ───────────┤                 │
   admin token       │                                ├──▶ backend      │
 peers ──────────────┼─▶ A2A server   /a2a/<agent>/ ──┤    + SQLite     │
   token issued      │                                │                 │
   to them           │   A2A client ◀─────────────────┘                 │
                     └──────┬───────────────────────────────────────────┘
                            └─▶ peers, with the token they issued
```

Each person runs one toli daemon, a single long-running process with a SQLite database. It holds that person's agents, the peers each agent may work with, and every task they send or receive. Agents talk to it over MCP on localhost. Daemons talk to each other over A2A. The person manages it with the TUI or the CLI, which use its admin API.

The daemon keeps working while its agents aren't running: tasks from peers are stored, and the agent is woken (or sees them the next time it checks).

A2A is two parts: the **A2A server** answers peers (tasks they send the daemon's agents, and the agents' cards), and the **A2A client** sends the agents' tasks to peers and polls them for updates. Pairing two agents swaps a token each way: each side checks the token it issued, and sends the one it was given.

## Listeners

The daemon serves three HTTP listeners:

- **MCP** (`127.0.0.1:7420`): agents' tools at `/mcp/<token>`, and the hook API at `/hook/` (sessions registering for notifications). Authorized by the agent's token.
- **Admin API** (`127.0.0.1:7421`): `/v1/`, for the TUI and CLI. Authorized by the admin token in `~/.toli/admin-token`. Agent tokens are never accepted here.
- **A2A** (configurable, localhost by default): peers' requests and the Agent Cards. Authorized by per-peer tokens; the public card needs none.

MCP and admin are separate ports so a sandbox can let an agent reach one and not the other. Both reject browser requests (any `Origin` header).

## Code map

All code is under `internal/`, with the entry point in `cmd/toli` (the CLI, the daemon, and the TUI's launcher).

| Package | What it does |
|---|---|
| `backend` | The daemon's logic: agents, peers, sessions, tasks, ownership checks. The MCP server, admin API and hook API call it rather than the database. |
| `mcp` | The MCP server: the tools agents get ([docs/mcp-api.md](docs/mcp-api.md)). |
| `api` | The admin API (`/v1/`) and its Go client, used by the TUI and CLI. |
| `hook` | The hook API (`/hook/`), and `toli hook`, which harnesses' hooks run. |
| `backend/wake` | Waking agents: the session each agent is running, and delivering notifications to it. |
| `backend/peers` | An agent's peers: their rows, and their A2A clients. |
| `a2aserver` | The A2A server: tasks peers send our agents. |
| `taskclient` | The A2A client side: tasks our agents send peers, and the latest state each peer reports. Storage is pluggable (`taskclient/taskstore`). |
| `peerclient` | One A2A client per peer, and the periodic check of each peer's card. |
| `store` | The only package that talks SQL. |
| `connect` | `toli connect` and `disconnect`: writing a harness's MCP config and hooks. |
| `service` | Running the daemon as a login service (launchd, systemd, Task Scheduler). |
| `tui` | The terminal UI, a client of the admin API like any other. |
| `token` | Making and hashing tokens. Only hashes are stored. |
| `a2atest` | An in-process A2A peer for tests. |

## How a task flows

Say Alice's agent asks Bob's agent to add an endpoint.

1. Alice's agent calls the MCP tool `delegate`. The backend checks that Bob's agent is one of its peers and hands the message to `taskclient`.
2. `taskclient` sends it to Bob's daemon over A2A and stores the task Bob's daemon returns.
3. Bob's daemon (`a2aserver`) checks Alice's token, stores the task, and asks `wake` to tell Bob's agent.
4. Bob's agent calls `list_available_tasks`, `accept_task`, then `update_task` as it works. Each update changes the task on Bob's daemon.
5. Alice's daemon polls the task every few seconds. Each report replaces its copy, and changes are passed to Alice's agent as notifications. The agent reads them with `get_delegated_task_updates`, and answers questions with `reply`.

Either side can cancel. A task has a time limit (an hour by default); past it, Alice's daemon asks Bob's to cancel it.

## Design decisions

**The human decides who an agent talks to.** Each agent has a list of peers that only its human edits, through the admin API. The daemon enforces it, and nothing an agent can call widens it. A peer on the list is trusted: its tasks follow plain A2A.

**The peer's last report is the truth.** When we send a task, we keep the latest copy the peer reported and never work out a state ourselves. A2A gives polled reports no order, and our own replies race the poller, so each kind of save has a rule for rejecting stale reports: a final state is never replaced; a poll is rejected if it lacks the newest message we have; a reply's result is rejected if that reply is already stored. Saves are compare-and-swap on a version number.

**Polling only.** We poll the tasks we sent rather than take pushes or streams. That works even when we can reach a peer and it can't reach us. Our card says `pushNotifications: false`. Push or streaming could later be added as more sources of the same reports.

**Sessions are A2A context IDs.** `start_session` makes one, and tasks delegated in it carry it. A peer must keep a context ID we send (or reject the task), so tasks group the same way on both sides.

**Deletes are hard.** Removing a peer, agent, or session deletes it and its tasks in one transaction; then peers are asked, best effort, to cancel the ones still running. No new work can start on something after it's deleted.

**A2A 1.0, with 0.3 at the edges.** toli speaks A2A 1.0. Peers on 0.3 work too: their cards and calls are translated at the edges (a2a-go's compatibility package), so everything inside stays 1.0.

**One binary.** Everyone who uses toli runs the daemon, so it's a single static Go binary, with SQLite in pure Go (no cgo).

## Security model

Agents get tokens that only reach their own work; the human holds the admin token. These keep out peers, browsers, other users, and anything coming in over MCP. They don't stop an agent that has a full shell as its human: it could read `~/.toli` or call the admin API. Agents should run sandboxed, allowed to reach toli's MCP port only. See [Security and trust](README.md#security-and-trust).

## Tests

`go test ./...` runs everything. Most packages have unit tests; `a2atest` stands in for a peer. The `e2e` package builds the `toli` binary and runs real daemons against each other: pairing, delegating, asking and answering, cancelling, the CLI, and notifications.
