# Contributing to toli

Thanks for helping. Bug reports, fixes, docs, and new harness integrations are all welcome.

## Questions and ideas

Ask on [Discord](https://discord.gg/DJ6p9DFXcB) or in [GitHub Discussions](https://github.com/arjunvlama/toli/discussions). For a bug, open an issue. For anything bigger than a small fix, open an issue or discussion first so we can agree on the approach before you write the code.

Found a security problem? Don't open an issue: see [SECURITY.md](SECURITY.md).

## Getting started

You need Go (the version in `go.mod`).

```sh
git clone https://github.com/arjunvlama/toli
cd toli
go build ./cmd/toli
go test ./...
```

`go test ./...` includes the end-to-end tests in `e2e/`, which build the `toli` binary and run real daemons against each other.

To try your build, run the daemon in the foreground with its own home and ports, so it doesn't touch your installed toli:

```sh
TOLI_HOME=/tmp/toli-dev ./toli daemon --mcp-listen 127.0.0.1:7520 --admin-listen 127.0.0.1:7521
TOLI_HOME=/tmp/toli-dev TOLI_ADMIN_ADDR=127.0.0.1:7521 TOLI_MCP_ADDR=127.0.0.1:7520 ./toli agent list
```

[ARCHITECTURE.md](ARCHITECTURE.md) explains how the code is organized and why. Read it before a larger change.

## Pull requests

- Keep each pull request to one change, and explain what it changes and why.
- Add or update tests for the behavior you change.
- Run `gofmt` and `go vet ./...`, and make sure `go test ./...` passes.
- Update the README or docs if you change how something is used.
- Write comments and messages in plain language, matching the code around them.

CI runs the tests on Linux and Windows.

## Adding a harness

toli connects coding agents through `toli connect` (`internal/connect`, one file per harness) and wakes them through `internal/backend/wake`. If you'd like to add one, open an issue first describing how the harness can be woken: a hook, a plugin API, a CLI command, or a socket.

## License

toli is licensed under [Apache-2.0](LICENSE). By contributing, you agree that your contributions are licensed under it too.
