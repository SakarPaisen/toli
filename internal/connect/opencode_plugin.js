// toli: registers this OpenCode session with your toli daemon, and delivers
// toli's notes (new work from peers, updates on tasks you delegated) into it
// as messages. Written by `toli connect opencode`; running it again replaces
// this file.
const TOLI = "{{HOOK_URL}}" // the daemon's hook endpoint for one agent

export const Toli = async ({ client }) => {
  let session = "" // the session toli knows about
  let context = "" // the agent's pending work, from registering the session
  let polling = false
  const busy = new Set() // sessions that are running (any, registered or not)
  const pending = []

  const post = (path, body) =>
    fetch(`${TOLI}/${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })

  // register makes id the agent's session (the newest one wins) and starts
  // collecting its notes.
  async function register(id) {
    if (id === session) return
    try {
      const res = await post("session-start", { harness: "opencode", session_id: id })
      if (!res.ok) return
      context = (await res.json()).context ?? ""
    } catch {
      return // daemon not running: nothing to do
    }
    session = id
    if (!polling) poll()
    deliver()
  }

  // poll keeps one request open to the daemon, which answers when notes
  // arrive (or after ~30s with none).
  async function poll() {
    polling = true
    while (session) {
      const id = session
      try {
        const res = await fetch(`${TOLI}/notifications?session=${encodeURIComponent(id)}`)
        if (res.status === 410) {
          if (session === id) session = "" // another session took over
          break
        }
        if (!res.ok) throw new Error(`toli: ${res.status}`)
        const { notifications: notes } = await res.json()
        pending.push(...notes)
        deliver()
      } catch {
        await new Promise((r) => setTimeout(r, 5000))
      }
    }
    polling = false
  }

  // deliver sends what's waiting, once the session is idle (a prompt sent
  // while it's busy may never run): the agent's pending work as context, which
  // starts no turn (as hooks do at session start elsewhere), then the notes
  // as one message.
  async function deliver() {
    const id = session
    if (!id || busy.has(id) || (!context && pending.length === 0)) return
    busy.add(id)
    const info = context
    const text = pending.splice(0).join("\n\n")
    context = ""
    try {
      // Keep the session's agent and model (a prompt without them switches
      // the session to the defaults).
      const msgs = await client.session.messages({ path: { id } })
      const last = (msgs.data ?? []).map((m) => m.info).filter((i) => i.role === "user").pop()
      const keep = { ...(last?.agent && { agent: last.agent }), ...(last?.model && { model: last.model }) }
      if (info) await client.session.prompt({ path: { id }, body: { ...keep, noReply: true, parts: [{ type: "text", text: info }] } })
      if (text) await client.session.promptAsync({ path: { id }, body: { ...keep, parts: [{ type: "text", text }] } })
      else busy.delete(id)
    } catch {
      // Try again when the session is next idle.
      if (info) context ||= info
      if (text) pending.unshift(text)
      busy.delete(id)
    }
  }

  return {
    event: async ({ event }) => {
      const p = event.properties ?? {}
      switch (event.type) {
        case "session.created":
        case "session.updated":
          // A top-level session the user is working in (not a subagent's).
          if (!p.info?.parentID) await register(p.info.id)
          break
        case "session.status":
          if (p.status?.type === "idle") busy.delete(p.sessionID)
          else busy.add(p.sessionID)
          if (p.sessionID === session) deliver()
          break
        case "session.idle":
          busy.delete(p.sessionID)
          if (p.sessionID === session) deliver()
          break
        case "session.deleted":
          if (p.info?.id === session) {
            post("session-end", { session_id: session }).catch(() => {})
            session = ""
          }
          break
      }
    },
  }
}
