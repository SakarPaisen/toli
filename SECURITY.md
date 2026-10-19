# Security

## Reporting a vulnerability

Please report security problems privately, not in a public issue. Use GitHub's private reporting: on this repository, go to **Security**, then **Report a vulnerability**.

Include what you found, how to reproduce it, and what an attacker could do with it. We'll acknowledge your report within a week, work on a fix with you, and credit you in the advisory unless you'd rather we didn't.

## What's in scope

toli's security model is described in the README's [Security and trust](README.md#security-and-trust) section. In short:

- Anyone who can reach the A2A port without a valid peer token should get nothing beyond the public Agent Cards.
- A peer should only reach the tasks it's part of, on the agents that allowed it.
- An agent's token should only reach that agent's own work, and never change its peers.
- Web pages, other users on the machine, and agents that only reach the MCP port should not reach the admin API.

Reports that break any of these are in scope.

Out of scope: an agent running unsandboxed as the same user as toli (it can read `~/.toli` directly, which toli can't prevent), and attacks that need a peer you've added to misbehave in ways A2A allows (peers you add are trusted).

## Supported versions

Fixes go into the latest release.
