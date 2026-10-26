# kanban

A self-hosted, Trello-style kanban board. One Go binary, one SQLite file, and plain
HTML/CSS/JS pages served by nginx.

> **Status: planning.** There is no code yet. See [PLAN.md](PLAN.md) for the build
> order.

## Why this stack

- **One binary, one file.** No database server, no `node_modules`, no container. Back
  it up by streaming one SQLite file with Litestream.
- **Fast on modest hardware.** Go uses every core in one process. SQLite runs in the
  same process, so a query is a function call rather than a network round trip.
- **Plain pages.** The frontend is vanilla ES modules with no build step. nginx serves
  them directly.

[SCALING.md](SCALING.md) explains the design and how far it scales on a single
server.

## Planned features (v1)

- Boards, lists and cards with drag-and-drop ordering
- Card details: Markdown description, labels, due dates, checklists, comments
- Signup restricted to configured email domains, with email verification
- Board members with owner, editor and viewer roles
- Live updates across open tabs (server-sent events)
- Per-board activity feed

## Architecture

```
browser ─► nginx ─┬─ /      static pages (web/)
                  └─ /api/  ─► kanban (Go, 127.0.0.1) ─► SQLite (WAL) ─► Litestream backups
```

## Development

*Once the M0 milestone lands:*

```bash
make run     # go run ./cmd/kanban -static ./web  → http://127.0.0.1:6220
make test    # go test -race ./...
make build-linux
```

The dev server serves `web/` itself, so you don't need nginx locally.

## Docs

| File | Contents |
|---|---|
| [PLAN.md](PLAN.md) | Scope, architecture, data model, API, milestones |
| [SCALING.md](SCALING.md) | High-throughput, single-server design guide |

## License

TBD
