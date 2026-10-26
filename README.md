# kanban

A self-hosted, Trello-style kanban board. One Go binary, one SQLite file, and plain
HTML/CSS/JS pages served by nginx.

> **Status:** boards, lists, cards, drag and drop, card details and live updates
> work. There is no login yet: everyone acts as one built-in user. See
> [PLAN.md](PLAN.md#status).

## Why this stack

- **One binary, one file.** No database server, no `node_modules`, no container. Back
  it up by streaming one SQLite file with Litestream.
- **Fast on modest hardware.** Go uses every core in one process. SQLite runs in the
  same process, so a query is a function call rather than a network round trip.
- **Plain pages.** The frontend is vanilla ES modules with no build step. nginx serves
  them directly.

[SCALING.md](SCALING.md) explains the design and how far it scales on a single
server.

## Features

- Boards, lists and cards with drag-and-drop ordering
- Card details: Markdown description, labels, due dates, checklists, comments
- *(next)* Signup restricted to configured email domains, with email verification
- *(next)* Board members with owner, editor and viewer roles
- Live updates across open tabs (server-sent events)
- Per-board activity feed

## Architecture

```
browser ─► nginx ─┬─ /      static pages (web/)
                  └─ /api/  ─► kanban (Go, 127.0.0.1) ─► SQLite (WAL) ─► Litestream backups
```

## Development

Needs Go 1.24 or newer (it fetches the exact toolchain `go.mod` asks for).

```bash
make run          # http://127.0.0.1:6220, data in ./data/kanban.db
make test         # go test -race ./...
make lint
make build-linux  # bin/kanban-linux-amd64, a static binary you can build from macOS
```

`make run` serves `web/` itself, so you don't need nginx locally. Delete `./data` to
start over.

The browser smoke test drives the whole UI in two tabs. Run it against a server on a
fresh data directory, with Playwright installed:

```bash
make e2e          # screenshots land in bin/shots/
```

## Docs

| File | Contents |
|---|---|
| [PLAN.md](PLAN.md) | Scope, architecture, data model, API, milestones |
| [SCALING.md](SCALING.md) | High-throughput, single-server design guide |

## License

TBD
