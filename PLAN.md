# Implementation Plan

A Trello-style kanban app: a Go API over SQLite, behind nginx, which serves the plain
HTML/CSS/JS pages. Design rationale lives in [SCALING.md](SCALING.md). This file is the
build order.

---

## 1. Scope

### v1: Trello parity, core loop

- Accounts: sign up, log in, log out
- Boards: create, rename, archive, list, star
- Lists: create, rename, reorder, archive
- Cards: create, edit title and description, reorder within a list and between lists,
  archive
- Card details: labels, due date, checklists, comments
- Board members with roles: owner, editor, viewer
- Live updates: changes appear in every open tab without a refresh
- Activity feed per board

### Not in v1

Attachments, search, notifications/email, templates, automation (Butler-style rules),
power-ups, multiple workspaces in the UI (the schema keeps `workspace_id` anyway; see
SCALING.md §7), mobile app, public boards.

---

## 2. Architecture

```
browser
  │  HTTP (Tailscale)
  ▼
nginx
  ├── /            → static files from /srv/apps/kanban/web   (HTML, CSS, JS)
  ├── /api/        → proxy to 127.0.0.1:6220                  (JSON)
  └── /api/boards/{id}/events → proxy, buffering off          (SSE)
          │
          ▼
kanban (Go binary, systemd, binds 127.0.0.1 only)
  ├── HTTP handlers (net/http, stdlib routing)
  ├── store: read pool (N conns) + single writer goroutine (group commit)
  ├── hub: per-board SSE fan-out
  └── SQLite file: /srv/data/kanban/kanban.db (WAL)
          │
          ▼
Litestream → /mnt/data/backups/kanban
```

**The split:** nginx owns static files, TLS (if it's ever added), compression and
caching headers. Go owns only `/api/*`. In dev, the binary can serve `web/` itself
with `-static ./web`, so no nginx is needed locally.

### Stack

| Piece | Choice | Why |
|---|---|---|
| Language | Go, current stable | SCALING.md §3 |
| Routing | `net/http` `ServeMux` with method + path patterns | No framework needed |
| DB driver | `modernc.org/sqlite` | Pure Go; cross-compile from a Mac |
| SQL | Hand-written queries in `internal/store`; sqlc later if it grows | Fewer tools on day one |
| Migrations | Embedded `.sql` files, tracked with `PRAGMA user_version` | ~40 lines, no dependency |
| Passwords | `golang.org/x/crypto/argon2` (argon2id) | Current best practice |
| Sessions | Random 256-bit token in an `HttpOnly; SameSite=Lax` cookie; SHA-256 of it stored in `sessions` | A database leak doesn't leak live sessions |
| Frontend | Vanilla ES modules, no build step | "Plain pages" |
| Drag and drop | SortableJS, one vendored file in `web/vendor/` | Handles touch, auto-scroll and nested lists; hard to get right by hand |
| Live updates | Server-sent events (`EventSource`) | SCALING.md §8 |
| Backups | Litestream | SCALING.md §10 |

---

## 3. Repository layout

```
cmd/kanban/main.go          flags, config, wiring, graceful shutdown
internal/
  config/                   env + flags
  store/
    migrations/0001_init.sql …
    migrate.go              user_version runner
    db.go                   open read pool + writer, pragmas
    writer.go               single-writer group commit (SCALING.md §5)
    rank.go                 fractional indexing
    boards.go lists.go cards.go users.go …
  auth/                     argon2id, sessions, middleware
  api/                      handlers, JSON helpers, errors, rate limits
  hub/                      SSE pub/sub
web/
  index.html                board list
  board.html                a board
  login.html
  css/app.css
  js/api.js                 fetch wrapper, ETag, errors
  js/board.js               render + drag and drop + SSE
  js/boards.js  js/login.js js/card.js
  vendor/Sortable.min.js
deploy/
  kanban.service            systemd unit
  nginx-kanban.conf         nginx site
  litestream.yml
  deploy.sh
scripts/
  loadtest.js               k6 scenario
SCALING.md  PLAN.md  README.md
```

---

## 4. Data model

All tables are `STRICT`. Times are Unix milliseconds (`INTEGER`). Every row with
user content has `archived_at INTEGER NULL`; Trello archives rather than deletes.

```sql
users        (id, email UNIQUE, name, pw_hash, created)
sessions     (token_hash PK, user_id, created, expires, last_seen)
workspaces   (id, name, created)                          -- one row in v1
boards       (id, workspace_id, title, color, created_by, created, archived_at, version)
board_members(board_id, user_id, role CHECK (role IN ('owner','editor','viewer')), PK(board_id,user_id))
board_stars  (board_id, user_id, PK(board_id,user_id))
lists        (id, board_id, title, rank TEXT, created, archived_at, version)
cards        (id, board_id, list_id, title, description, rank TEXT, due INTEGER NULL,
              due_done INTEGER, created_by, created, updated, archived_at, version)
labels       (id, board_id, name, color)
card_labels  (card_id, label_id, PK(card_id,label_id))
checklists   (id, card_id, title, rank TEXT)
check_items  (id, checklist_id, text, done INTEGER, rank TEXT)
comments     (id, card_id, user_id, body, created, edited)
events       (seq INTEGER PRIMARY KEY, board_id, user_id, type, payload TEXT /* JSON */, created)
idempotency  (key PK, user_id, response TEXT, created)
```

Indexes: `lists(board_id, rank)`, `cards(list_id, rank)`, `cards(board_id)`,
`events(board_id, seq)`, `comments(card_id, created)`, `board_members(user_id)`,
`sessions(expires)`.

**Design points:**

- `cards.board_id` is denormalized so board loads and permission checks don't need a
  join through `lists`.
- `boards.version` bumps on **any** change inside the board. It drives the `ETag`
  for `GET /api/boards/{id}`.
- `rank` is a fractional index (SCALING.md §7). **The server computes it.** Clients
  send `before_id` and `after_id`, never rank strings.
- `events` is the activity feed and the SSE replay log at once.

---

## 5. API

JSON in and out. Errors are `{"error": "message", "code": "conflict"}`. Every write
checks the caller's role on the board. Writes accept an optional `Idempotency-Key`
header.

### Auth

| Method | Path | |
|---|---|---|
| POST | `/api/auth/signup` | `{email, name, password}` |
| POST | `/api/auth/login` | Sets the session cookie |
| POST | `/api/auth/logout` | |
| GET | `/api/me` | Current user |

### Boards

| Method | Path | |
|---|---|---|
| GET | `/api/boards` | Boards the user is a member of, starred first |
| POST | `/api/boards` | `{title, color}` → the creator is owner |
| GET | `/api/boards/{id}` | **Full snapshot:** board, lists, cards (with label IDs and checklist counts), labels, members. One query batch. `ETag` / `304`. |
| PATCH | `/api/boards/{id}` | `{title?, color?, archived?}` |
| PUT/DELETE | `/api/boards/{id}/star` | |
| POST | `/api/boards/{id}/members` | `{email, role}` (owner only) |
| PATCH/DELETE | `/api/boards/{id}/members/{uid}` | |
| GET | `/api/boards/{id}/activity?before=seq` | Paged feed |
| GET | `/api/boards/{id}/events` | SSE stream; honors `Last-Event-ID` |

### Lists and cards

| Method | Path | |
|---|---|---|
| POST | `/api/boards/{id}/lists` | `{title}` → appended at the end |
| PATCH | `/api/lists/{id}` | `{title?, archived?, version}` |
| POST | `/api/lists/{id}/move` | `{before_id?, after_id?}` |
| POST | `/api/lists/{id}/cards` | `{title}` → appended at the end |
| GET | `/api/cards/{id}` | Card detail: description, checklists, comments |
| PATCH | `/api/cards/{id}` | `{title?, description?, due?, due_done?, archived?, version}` → `409` on a stale version |
| POST | `/api/cards/{id}/move` | `{list_id, before_id?, after_id?}` |
| PUT/DELETE | `/api/cards/{id}/labels/{label_id}` | |
| POST | `/api/cards/{id}/comments` | |
| POST | `/api/cards/{id}/checklists` | Checklist and item routes follow the same pattern |
| POST/PATCH/DELETE | `/api/boards/{id}/labels[/{label_id}]` | |

### Ops

| Method | Path | |
|---|---|---|
| GET | `/healthz` | Pings the database; no auth |
| GET | `/debug/pprof/*` | Separate listener on `127.0.0.1:6221`, never proxied |

---

## 6. Frontend

Three pages, no build step:

- **`login.html`:** sign in and sign up.
- **`index.html`:** a grid of boards, starred first, plus "New board."
- **`board.html?id=N`:** horizontal lists, with cards in each list.
  - Inline add-card and add-list forms.
  - Click a card to open a modal with description, labels, due date, checklists and
    comments.
  - Drag cards and lists with SortableJS.
  - On drop, update the UI at once, send `POST …/move` with neighbor IDs, and roll
    back with a toast if the server rejects it.
  - Subscribe to `/api/boards/{id}/events`. Apply incoming events to the in-memory
    model and re-render the affected list. Ignore the client's own echoes, matched
    by a per-tab `client_id` sent in a header and carried on events.

**State:** one in-memory model per board (`Map`s of lists, cards and labels),
rendered with small template functions. If a list grows past a few hundred cards,
revisit rendering; don't optimize before then.

**Caching:** nginx serves `*.html` with `Cache-Control: no-cache` (always revalidate),
and serves `css/` and `js/` with an `ETag` and a short max-age. Cache-bust with a
`?v=<git sha>` query string, which `deploy.sh` stamps into the HTML.

---

## 7. Milestones

Each milestone ends with something you can run and click through.

### M0: Skeleton
- `go.mod`, `cmd/kanban`, config, graceful shutdown
- Store: open the read pool and writer with pragmas, migration runner, `0001_init.sql`
- Writer goroutine with tests: batching, savepoint isolation, context cancellation
- `/healthz`, `-static ./web` dev mode
- `Makefile`: `run`, `test`, `build-linux`, `lint` (`go vet`, `staticcheck`)
- GitHub Actions: vet, test, build

**Done when:** `make run` serves a placeholder page, `/healthz` reports ok, and CI
is green.

### M1: Auth
- Signup, login and logout; argon2id; session cookie; auth middleware
- A per-IP rate limit on login
- Login page

**Done when:** you can sign up, log in, and `/api/me` returns you.

### M2: Boards, lists, cards (no drag yet)
- `rank.go` with property tests: `between(a, b)` always sorts strictly between its
  arguments, and keys stay short over repeated inserts
- Board, list and card CRUD, role checks, `version` + `409`
- Board snapshot endpoint with ETag
- Board list page and board page; render, add, rename, archive

**Done when:** you can build a board with lists and cards, and reload to see it
persist.

### M3: Drag and drop
- Move endpoints for cards and lists
- SortableJS wiring, optimistic UI with rollback

**Done when:** dragging cards within and between lists, and dragging lists, survives
a reload. Two quick drags don't produce out-of-order results.

### M4: Live updates and activity
- The `hub` package; events written in the same transaction as the change and
  published after commit
- SSE endpoint with `Last-Event-ID` replay, heartbeats, and dropping slow
  subscribers
- Activity sidebar

**Done when:** two browsers on one board see each other's changes within about
100 ms, and a client that was offline catches up on reconnect.

### M5: Card details
- Card modal: description (plain text with line breaks in v1), due date, labels,
  checklists, comments

**Done when:** the card modal reaches feature parity with Trello's basics.

### M6: Members and permissions
- Invite by email (the user must already exist in v1), roles, a viewer read-only UI,
  leave or remove a member

**Done when:** a viewer cannot change anything. This is enforced on the server and
tested per endpoint.

### M7: Deploy to `elevate`
- `deploy/kanban.service`:
  - `User=kanban`, `127.0.0.1:6220`, `GOMEMLIMIT=128MiB`
  - Hardening: `ProtectSystem=strict`, `ReadWritePaths=/srv/data/kanban`,
    `NoNewPrivileges`
- `deploy/nginx-kanban.conf`: a Tailscale-only listen, static root, `/api/` proxy,
  an SSE location with `proxy_buffering off`, `proxy_read_timeout 1h`
- `deploy/litestream.yml` → `/mnt/data/backups/kanban`
- `deploy.sh`:
  1. `GOOS=linux go build`
  2. rsync the binary and `web/`
  3. `nginx -t && systemctl reload nginx`
  4. restart `kanban`
  5. curl `/healthz`
- Register with the rootstock agent (CloudServer's checklist, step 7)

**Done when:** a restore from Litestream has been tested. Section 9 covers Rule zero.

### M8: Hardening and load test
- `scripts/loadtest.js` (k6, open model, the 90/8/2 mix from SCALING.md §11)
- Seed script with realistic volumes
- Profile, fix the top items, and record the numbers in SCALING.md

**Done when:** a measured capacity and p99 are written down.

---

## 8. Testing

- **Store:** tests run against a real SQLite file in `t.TempDir()`, not mocks. Cover
  migrations, the writer, ranks, version conflicts and permission queries.
- **API:** `httptest` end-to-end tests per endpoint, including role checks (a table
  of role × endpoint → expected status).
- **Concurrency:** run everything with `go test -race`. Add a test with 100
  concurrent card moves that asserts the final order is consistent.
- **Frontend:** a Playwright smoke test (log in → create a board → add a card → drag
  it → reload → assert) in CI from M3 on.

---

## 9. Deployment notes for `elevate`

- **Ports:** the app on `127.0.0.1:6220` and pprof on `127.0.0.1:6221`. nginx
  listens on the Tailscale address. Pick an unused external port, or a `server_name`
  such as `kanban.elevated.eco`, following the existing `*.elevated.eco` pattern.
  Confirm against `intranet.conf` first.
- **Paths:** binary at `/srv/apps/kanban/kanban`, static files at
  `/srv/apps/kanban/web`, data at `/srv/data/kanban/`, backups at
  `/mnt/data/backups/kanban/`.
- **Rule zero:** this deploy only adds an nginx site, reloads nginx, and starts one
  systemd unit. It touches no netplan, networkd, firewall or Tailscale settings, so
  it can't interrupt the server's internet access. Keep it that way: `deploy.sh` must
  never contain network commands.
- **The repo is public:** no hostnames, IPs or secrets in committed config beyond
  what's already public. Keep host-specific values in a gitignored `deploy/.env`.

---

## 10. Open questions

1. **Auth:** local accounts (this plan), or trust Tailscale identity? `tailscale
   whois` on the peer address would allow zero-password single sign-on, but only
   inside the tailnet. Local accounts keep the app portable for a public repo;
   Tailscale identity could be added later as a second login method.
2. **Hostname/port** on `elevate` (§9).
3. **Markdown in card descriptions:** v1 renders plain text. Rendering Markdown
   safely means a sanitizer, so it's deferred.
4. **Signup:** open, invite-only, or admin-created accounts? The default here is open
   signup, with an env flag to disable it.
