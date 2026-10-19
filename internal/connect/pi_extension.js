// toli: adds toli's MCP server to Pi, registers each session with your toli
// daemon, and delivers toli's notes (new work from peers, updates on tasks
// you delegated) into it as messages. Written by `toli connect pi`; running
// it again replaces this file.
const MCP = "{{MCP_URL}}" // the agent's MCP URL
const TOLI = "{{HOOK_URL}}" // the daemon's hook endpoint for the same agent

export default function (pi) {
  pi.registerMcpServer("toli", { url: MCP })

  let session = "" // the session toli knows about
  let poller = null // aborts the current session's polling

  const post = (path, body) =>
    fetch(`${TOLI}/${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })

  // poll keeps one request open to the daemon, which answers when notes
  // arrive (or after ~30s with none). A note sent while Pi is busy waits for
  // the current run to end.
  async function poll(id, signal) {
    while (!signal.aborted) {
      try {
        const res = await fetch(`${TOLI}/notifications?session=${encodeURIComponent(id)}`, { signal })
        if (res.status === 410) return // another session took over
        if (!res.ok) throw new Error(`toli: ${res.status}`)
        const { notifications: notes } = await res.json()
        if (notes.length > 0) pi.sendUserMessage(notes.join("\n\n"), { deliverAs: "followUp" })
      } catch {
        if (signal.aborted) return
        await new Promise((r) => setTimeout(r, 5000))
      }
    }
  }

  // Every session (started, new, resumed, forked, reloaded) becomes the
  // agent's session; the newest one wins.
  pi.on("session_start", async (_event, ctx) => {
    poller?.abort()
    const id = ctx.sessionManager.getSessionId()
    let context
    try {
      const res = await post("session-start", { harness: "pi", session_id: id })
      if (!res.ok) return
      context = (await res.json()).context
    } catch {
      return // daemon not running: nothing to do
    }
    // The agent's pending work, shown to the model with the next turn
    // (without starting one), as hooks do at session start elsewhere.
    if (context) pi.sendMessage({ customType: "toli", content: context, display: true }, { deliverAs: "nextTurn" })
    session = id
    poller = new AbortController()
    poll(id, poller.signal)
  })

  pi.on("session_shutdown", async (event) => {
    poller?.abort()
    poller = null
    if (event.reason === "quit" && session) {
      await post("session-end", { session_id: session }).catch(() => {})
    }
    session = ""
  })
}
