# toli MCP API (v1)

The tools a coding agent sees when connected to its toli daemon. An agent
uses them in two roles: **delegating** (sending work to peers) and **working**
(doing work peers sent it). Every tool maps onto an A2A operation or onto the
daemon's stored tasks; toli adds nothing to the A2A wire protocol.

## Connection and identity

- Transport: MCP Streamable HTTP at `http://127.0.0.1:7420/mcp/<token>`
  (or `/mcp` with `Authorization: Bearer <token>`).
- The token identifies one **local agent**. Every call acts as that agent: its
  allowlist, its tasks. Nothing is tied to a particular connection, and toli
  doesn't tell connections apart: one agent is one working session. For
  parallel work, add another agent.
- The server's `initialize` result carries `instructions`:

  > Call `list_peers` to see who you can delegate to. Use `delegate`, then
  > `get_delegated_task_updates` with `after_version` to follow a task. At the
  > start of a session, call `list_accepted_tasks` to resume your work and
  > `list_available_tasks` for new work. Take new work on with `accept_task`,
  > or decline it with `update_task(…, "rejected")`. Then `update_task`:
  > `"working"` for progress, `"input-required"` to ask, `"completed"` to
  > finish.

## Conventions

- **Task IDs** are A2A task IDs. If the agent loses track of them (new
  conversation, cleared context), `list_delegated_tasks` and
  `list_accepted_tasks` find them again.
- **Sessions** group related tasks. A delegating agent creates them with
  `start_session`; the session is the A2A `contextId` sent with each task, so
  both sides see which tasks belong together. A task delegated without one is
  **standalone**.
- **States** are A2A's: `submitted`, `working`, `input-required`, `completed`,
  `failed`, `canceled`, `rejected`. The last four are *final*: nothing changes
  a final task. (A2A's `auth-required` isn't used.)
- **Turns.** A2A is turn-based: a worker that needs something pauses with
  `input-required` and one message (all its questions together); the
  delegator answers and the task resumes. A worker has at most one open
  question. A delegator may also send a message on an unfinished task at any
  other time.
- **Versions.** Every task has a `version` that goes up whenever it changes.
  Action results include it; pass it as `after_version` to wait for the next
  change.
- **Errors** are MCP tool errors (`isError: true`) with a plain message, e.g.
  `peer "bob": not found`, `task t_123 not found`,
  `task t_123 was canceled by bob; stop working on it`.
- v1 messages and artifacts are text only.

## Delegating

### `list_peers()`
Who this agent may delegate to.
Returns `{peers: [{peer, name, description, skills, reachable}]}`. `peer` is
your alias for it, to pass to `delegate`; `name` is what the peer calls itself
(from its Agent Card); `reachable` is false if the daemon's last check of it
failed.

### `start_session(title)`
Start a session to group related tasks, e.g. "users api".
Returns `{session_id, title}`.

### `delete_session(session_id)`
Done with a session: removes it and its tasks right away, and asks peers to
cancel the tasks still running (best effort, in the background).
Returns `{deleted: true}`.

### `delegate(peer, message, session_id?, time_limit_minutes?)`
Start a task on a peer. A2A `SendMessage`. `time_limit_minutes` (default 60)
sets a deadline: if the task isn't finished by then, the daemon cancels it on
the peer.
Returns `{task_id, state, version, answer?}`. If the peer answers directly
instead of creating a task (allowed by A2A), `answer` is `{from, text}` and the
exchange is recorded as an already-`completed` task (`direct` in its full view).

### `get_delegated_task_updates(task_id, after_version?, wait_seconds?)`
Where a delegated task stands now. Without `after_version`, returns at once;
with it, waits (`wait_seconds`, default 60, max 120) until the task is newer,
then returns either way.

```json
{
  "task_id": "t_123",
  "peer": "bob",
  "session_id": "s_9",
  "state": "input-required",
  "messages": [
    "Should GET /users include deleted users?",
    "And should it page by cursor or by offset?"
  ],
  "artifacts": [{"name": "diff", "text": "..."}],
  "version": 5,
  "updated_at": "2026-10-12T14:04:00Z",
  "deadline": "2026-10-12T15:00:00Z"
}
```

`messages` are everything the worker said since your last message to it,
oldest first: its open question, progress notes, its final answer.
`artifacts` are its outputs so far.

### `reply(task_id, message)`
Answer the worker's question, or tell it something while it works. A2A
`SendMessage` on the task.
Returns `{task_id, state, version, answer?}` (`answer` if the peer responded
directly with a message).

### `cancel_task(task_id)`
Stop a delegated task. A2A `CancelTask`.
Returns `{task_id, state, version}`: `canceled`, or the final state it had
already reached.

### `list_delegated_tasks(session_id?, state?)`
Tasks this agent delegated, newest first.
Returns `{tasks: [{task_id, peer, session_id, state, summary, updated_at}]}`;
`summary` is the first line of the request.

## Working

### `list_available_tasks(wait_seconds?)`
New work peers sent this agent that it hasn't accepted yet (`submitted`),
newest first. With `wait_seconds` (max 120): if there's none, wait until some
arrives.

```json
{
  "tasks": [
    {
      "task_id": "t_456",
      "peer": "bob",
      "session_id": "s_9",
      "request": "Add a /users endpoint with cursor pagination",
      "state": "submitted",
      "unanswered_messages": 0,
      "version": 1,
      "updated_at": "2026-10-12T14:02:00Z"
    }
  ]
}
```

`session_id` is the task's A2A `contextId`: tasks with the same one belong
together (a peer that sends none gets a fresh one per task).

### `accept_task(task_id, message?)`
Take on new work: `submitted` → `working`. `message` is an optional note to
the delegator (e.g. your plan).
Returns `{task_id, state, version, request, messages}`: `messages` is anything
else the delegator said since its request.
Errors: already accepted; canceled by its delegator; already final; task not
found.

### `list_accepted_tasks(state?)`
Work this agent accepted, newest first; by default what's still in progress
(`working`, `input-required`). `state` picks another (e.g. `completed`). Rows
are as in `list_available_tasks`; `unanswered_messages` counts the
delegator's messages since the agent's last message on the task.

### `update_task(task_id, state, message?, artifacts?, wait_seconds?)`
Everything the worker tells the delegator about accepted work, and declining
new work.

| `state` | Use |
|---|---|
| `working` | post progress; respond to the delegator; carry on without an answer |
| `input-required` | ask: all questions in one `message`. With `wait_seconds` (max 120), wait for the answer and return it as `answer`; otherwise it arrives later, as a notification. |
| `completed` | finish: summary in `message`, outputs in `artifacts: [{name, text}]` (a name already sent replaces that output) |
| `failed` | give up: reason in `message` |
| `rejected` | decline new work (not yet accepted): reason in `message` (required) |

Returns `{task_id, state, version, answer?}`.
Errors: not accepted yet (except `rejected`); a question is already open; the
task was canceled by its delegator; the task is already final; task not
found.

## Shared

### `get_task_full(task_id)`
Everything about a task, delegated or assigned: the whole conversation, with
each message labeled by its sender's name, and all artifacts. `direction` is
`delegated` or `assigned`; `direct` and `deadline` are only on delegated
tasks.

```json
{
  "task_id": "t_123",
  "direction": "delegated",
  "peer": "bob",
  "session_id": "s_9",
  "state": "completed",
  "messages": [
    {"from": "backend", "text": "Add a /users endpoint"},
    {"from": "bob", "text": "Should it include deleted users?"},
    {"from": "backend", "text": "Only active ones."},
    {"from": "bob", "text": "Done: added GET /users."}
  ],
  "artifacts": [{"name": "diff", "text": "..."}],
  "direct": false,
  "version": 6,
  "updated_at": "2026-10-12T14:10:00Z",
  "deadline": "2026-10-12T15:00:00Z"
}
```
