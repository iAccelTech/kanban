# Scaling

How this app is built to handle very high request throughput on **one server**: Go,
SQLite, and no distributed systems until the numbers force them.

The guide moves from theory to practice:

1. [Mental models](#1-mental-models): the math behind throughput and latency
2. [The single-server case](#2-the-single-server-case): who does this and why it works
3. [Runtime: Go vs Node](#3-runtime-go-vs-node)
4. [SQLite for throughput](#4-sqlite-for-throughput)
5. [Write path: single writer, group commit](#5-write-path-single-writer-group-commit)
6. [Read path](#6-read-path)
7. [Data modeling for a kanban board](#7-data-modeling-for-a-kanban-board)
8. [Realtime fan-out](#8-realtime-fan-out)
9. [Backpressure and load shedding](#9-backpressure-and-load-shedding)
10. [Durability and backups](#10-durability-and-backups)
11. [Measuring](#11-measuring)
12. [The scaling ladder](#12-the-scaling-ladder)
13. [Anti-patterns](#13-anti-patterns)
14. [Further reading](#14-further-reading)

> Numbers marked **~** are order-of-magnitude estimates for a modern 4–8 core machine
> with NVMe storage. They are for intuition only. Section 11 explains how to measure the
> real ones.

---

## 1. Mental models

### Throughput, latency, concurrency: Little's Law

```
concurrency = throughput × latency
```

At 5,000 requests per second, each taking 4 ms, about 20 requests are in flight at once.
The law works in every direction:

- To raise throughput at fixed concurrency (a connection pool, a worker count), you must
  **cut latency**.
- If latency rises (a slow query, a lock wait, a GC pause), the number of requests in
  flight rises with it. Requests pile up, queues grow, and memory grows.

This is why a single slow database query can take down an otherwise fast service.

### Utilization and queueing: why 90% busy is "full"

For a simple queue, average time in the system is:

```
latency ≈ service_time / (1 − utilization)
```

| Utilization | Latency multiplier |
|---|---|
| 50% | 2× |
| 80% | 5× |
| 90% | 10× |
| 99% | 100× |

Latency does not degrade linearly. It falls off a cliff. **Plan capacity so the
bottleneck resource sits around 50–70% at peak.** The bottleneck here is usually the
SQLite writer.

### Tail latency

Users feel the **p99** (the slowest 1%), not the average. A page that makes 20 backend
calls hits a p99 on about 18% of loads (1 − 0.99²⁰). Always report p50, p99 and p99.9,
never just the mean.

### Scalability limits: Amdahl and the Universal Scalability Law

Adding cores helps only for work that runs in parallel. Neil Gunther's USL models it:

```
C(N) = N / (1 + α(N − 1) + βN(N − 1))
```

- **α (contention):** time spent waiting on shared things, such as a lock or the single
  SQLite writer. This caps the speedup.
- **β (coherency):** cost of keeping copies in sync, such as cache-line bouncing or
  cross-node chatter. This makes throughput **go down** as you add more.

The single-server design here minimizes β completely: there are no nodes to keep in
sync. It then attacks α with batching (section 5).

### Know your latency budget

| Operation | ~Time |
|---|---|
| L1 cache / main memory reference | 1 ns / 100 ns |
| SQLite indexed point lookup (page in cache) | 1–10 µs |
| Go HTTP handler overhead | 10–50 µs |
| fsync, enterprise NVMe with power-loss protection | 20–100 µs |
| fsync, consumer NVMe | 0.5–5 ms |
| Same-datacenter network round trip | 0.5 ms |
| Tailscale round trip, same city | 5–30 ms |

Two lessons:

1. **In-process SQLite is about 100× closer than a network database.** A Postgres
   query costs at least one network round trip. A SQLite query is a function call.
2. **fsync is the expensive operation.** On consumer NVMe, each durable commit costs
   about a millisecond, which caps you near 1,000 commits/s. Unless you batch.

---

## 2. The single-server case

Each of these organizations wrote publicly about running very large workloads on one
database server, or a handful:

| Who | Setup | Takeaway |
|---|---|---|
| **Expensify** | SQLite under their own C++ layer, Bedrock, on one large machine (192 cores, ~1 TB RAM). Reported ~4M queries/s. | SQLite is not a toy. With enough RAM, a single process scales reads across cores. |
| **Tailscale** | Moved its coordination server to SQLite, in Go. | A Go and SQLite control plane serving a large fleet. Simplicity beat a distributed store. |
| **Let's Encrypt** | One primary MariaDB server for certificate issuance at internet scale. | Buy a bigger box before you build a cluster. |
| **Stack Overflow** | Ran for years on a handful of Microsoft SQL Server machines. | Vertical scaling plus aggressive caching goes a very long way. |
| **37signals (ONCE)** | Ships self-hosted products on SQLite. | One file, one binary, no operations team. |

**Why it works:**

- **Hardware got huge.** A single server can have hundreds of cores, terabytes of RAM
  and millions of IOPS. Most "big" datasets fit in RAM.
- **Coordination is the enemy of throughput.** Every network hop, consensus round and
  distributed lock adds latency and failure modes. One machine has none of that.
- **Operational simplicity compounds.** One process to deploy, monitor, back up and
  reason about.

**What you give up:** high availability (if the box dies, you're down until it's
restored) and unlimited write scale. Section 10 covers the first; section 12 says
when the second matters.

---

## 3. Runtime: Go vs Node

| | Go | Node |
|---|---|---|
| **Concurrency** | Goroutines across all cores in one process | One event-loop thread; scale with `cluster` or `worker_threads` |
| **Plain JSON endpoint** | ~50k–150k req/s | ~15k–40k req/s per process |
| **Blocking work** | One slow handler blocks only its own goroutine | A sync call (e.g. `better-sqlite3`) blocks **every** request |
| **Memory** | ~15–30 MB baseline, a few KB per goroutine | ~50–100 MB per process, multiplied by the core count when clustered |
| **GC** | Low-pause, concurrent; `GOMEMLIMIT` sets a hard target | Generational; heap tends to grow before collection |
| **Deploy** | One static binary, frontend embedded with `embed.FS` | Runtime + `node_modules` + native addon builds |

**The core difference:** Go turns a multi-core machine into one big parallel process.
Node turns it into N separate processes that each hold their own memory and their
own database connections. For a single-server design, Go uses the box more fully.

**Node's strengths:** it is excellent at holding many idle connections (long-poll,
SSE), and it shares a language with the frontend.

---

## 4. SQLite for throughput

### Connection settings

```go
const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=cache_size(-65536)" + // 64 MiB page cache per connection (negative = KiB)
	"&_pragma=mmap_size(268435456)" + // 256 MiB memory-mapped reads
	"&_pragma=temp_store(MEMORY)"
```

Driver: `modernc.org/sqlite`, which is pure Go, so it builds for Linux from any OS
without a C toolchain. `mattn/go-sqlite3` (cgo) runs roughly 1.5–2× faster on hot
loops. Switch only if profiling shows the driver itself is the bottleneck.

### What each setting buys

| Setting | Effect |
|---|---|
| `journal_mode=WAL` | Readers never block the writer and the writer never blocks readers. **This is the single most important setting.** |
| `synchronous=NORMAL` | In WAL mode, fsync happens at checkpoint instead of every commit. Safe against **app** crashes. On **power loss** you may lose the last few transactions, but the database never corrupts. `FULL` fsyncs every commit. |
| `busy_timeout` | Wait for a lock instead of failing at once with `SQLITE_BUSY`. |
| `cache_size` | The page cache. Keep the hot working set in it. |
| `mmap_size` | Reads go through the OS page cache with no copy. A big win for read-heavy loads. |
| `BEGIN IMMEDIATE` | Take the write lock at transaction start. Avoids deadlock-style `SQLITE_BUSY` when a read transaction tries to upgrade to a write. With modernc, set `_txlock=immediate` on the writer's connection string. |

### The one rule: SQLite has exactly one writer

Only one write transaction can run at a time, in any language. You can't remove that
limit, so design around it:

- **Keep write transactions tiny.** No network calls, no file I/O, nothing slow inside
  a transaction.
- **Batch.** Many logical writes in one physical commit (section 5).
- **Make reads never wait.** WAL plus a separate read pool (section 6).

### Checkpoints and WAL growth

WAL appends to `kanban.db-wal`. A **checkpoint** copies pages back into the main file.
A long-running read transaction pins old pages and **stops the checkpoint from
finishing**, so the WAL grows without bound. Prevent this:

- Keep read transactions short. Never hold a `*sql.Rows` open while doing other work.
- Leave `wal_autocheckpoint` at its default (1000 pages), or run
  `PRAGMA wal_checkpoint(TRUNCATE)` from a background goroutine during quiet periods.
- Alert if the `-wal` file passes about 1 GB.

### Schema hygiene

- Use `STRICT` tables to get real type checking.
- Index every foreign key and every `WHERE`/`ORDER BY` column on hot paths. Check
  with `EXPLAIN QUERY PLAN`. A `SCAN` on a hot table is a bug.
- Use `INTEGER PRIMARY KEY` (the rowid) for the fastest lookups. If you need public
  IDs, use UUIDv7, which sorts by time and so keeps B-tree inserts local.
- Run `PRAGMA optimize` on connection close or periodically, to keep planner
  statistics fresh.

---

## 5. Write path: single writer, group commit

Funnel every write through **one goroutine** that owns the only write connection. It
drains whatever writes are queued and commits them together in **one transaction**. One
fsync covers the whole batch.

```
handlers ──► reqs chan (bounded) ──► writer goroutine ──► BEGIN … N writes … COMMIT ──► fsync ×1
                                                      └──► reply to each handler
                                                      └──► publish events (after commit)
```

```go
type writeReq struct {
	fn   func(*sql.Tx) error
	done chan error
}

type Writer struct {
	db   *sql.DB // SetMaxOpenConns(1), _txlock=immediate
	reqs chan writeReq
}

const maxBatch = 256

func (w *Writer) Do(ctx context.Context, fn func(*sql.Tx) error) error {
	r := writeReq{fn: fn, done: make(chan error, 1)}
	select {
	case w.reqs <- r:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err() // the write may still commit; make writes idempotent
	}
}

func (w *Writer) Run(ctx context.Context) {
	for {
		var batch []writeReq
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
		case <-ctx.Done():
			return
		}
	drain:
		for len(batch) < maxBatch {
			select {
			case r := <-w.reqs:
				batch = append(batch, r)
			default:
				break drain
			}
		}
		w.commit(batch)
	}
}

func (w *Writer) commit(batch []writeReq) {
	tx, err := w.db.Begin()
	if err != nil {
		for _, r := range batch {
			r.done <- err
		}
		return
	}
	errs := make([]error, len(batch))
	for i, r := range batch {
		// A savepoint per request: one bad request doesn't poison the batch.
		tx.Exec("SAVEPOINT req")
		if errs[i] = r.fn(tx); errs[i] != nil {
			tx.Exec("ROLLBACK TO req")
		}
		tx.Exec("RELEASE req")
	}
	err = tx.Commit()
	for i, r := range batch {
		if err != nil {
			r.done <- err
		} else {
			r.done <- errs[i]
		}
	}
}
```

**Why it's fast:**

- **Under light load,** a batch has one item, so there is no added latency.
- **Under heavy load,** batches grow on their own. 200 writes cost one commit instead
  of 200. That turns a ceiling of ~1k commits/s into **~10k–100k logical writes/s**.
  This is the same idea Postgres and MySQL call *group commit*.
- **There is no lock contention.** Only one goroutine ever touches the write
  connection, so `SQLITE_BUSY` on writes disappears.

**Rules:**

- `fn` must be pure database work: no network calls and no sleeping.
- Publish realtime events **after** `Commit` succeeds, never inside `fn`.
- The `reqs` channel is **bounded**. When it is full, `Do` waits until the request's
  context deadline passes. That deadline is your backpressure (section 9).

---

## 6. Read path

- **A separate `*sql.DB` for reads,** opened read-only (`mode=ro`, or
  `_pragma=query_only(1)`), with `SetMaxOpenConns(runtime.NumCPU())`. Reads run
  across all cores in parallel and never touch the writer.
- **Prepare statements once.** sqlc (generates Go from SQL) or a prepared-statement
  cache avoids re-parsing SQL on every call.
- **One query per screen.** Load a board with a single query, or a few, that returns
  lists, cards and labels together. Never one query per card (the N+1 pattern). JSON
  functions (`json_group_array`) can build nested results inside SQLite.
- **HTTP caching.** Give each board a `version` that bumps on every write. Serve
  `ETag: "board-<id>-v<version>"`. A client that is already current gets a
  `304 Not Modified` with no query and no serialization.
- **In-process caching only when measured.** SQLite's page cache plus mmap is already
  a memory cache. Add an application cache (e.g. rendered board JSON keyed by version)
  only if profiling shows serialization is the cost.

---

## 7. Data modeling for a kanban board

### Card ordering: fractional indexing

The naive approach stores `position INTEGER` and renumbers every card below a card
that moved. One drag then becomes N row updates, plus write amplification and
conflicts.

The better approach stores a **sortable string key** (fractional indexing, as in
LexoRank). Moving a card between `a0` and `a1` gives it the new key `a0V`, and only
**one row changes**.

```sql
CREATE TABLE cards (
  id       INTEGER PRIMARY KEY,
  list_id  INTEGER NOT NULL REFERENCES lists(id),
  rank     TEXT    NOT NULL,      -- fractional index
  title    TEXT    NOT NULL,
  version  INTEGER NOT NULL DEFAULT 1,
  updated  INTEGER NOT NULL
) STRICT;
CREATE INDEX cards_list_rank ON cards(list_id, rank);
```

Keys grow longer after many inserts at the same spot. Rebalance a list in the
background once a key passes, say, 50 characters.

### Concurrent edits: optimistic concurrency

Every row carries a `version`. Updates are conditional:

```sql
UPDATE cards SET title = ?, version = version + 1
WHERE id = ? AND version = ?;
```

Zero rows affected means someone else edited the card first. Return `409 Conflict`
and let the client refetch. This takes no locks and costs no extra round trips.

### Activity log: append-only

Store history as an append-only `events` table with an autoincrementing `seq`. It
serves three purposes:

- the activity feed in the UI;
- the realtime stream (section 8): clients resume from "everything after seq N";
- auditing, and replaying the history to debug problems.

Appends are the cheapest write a B-tree can do.

### Tenancy: database per workspace, the escape hatch

Each workspace gets its own SQLite file. You get:

- **Writes in parallel across workspaces.** Each file has its own writer, so the
  one-writer limit becomes "one writer per workspace."
- **Isolation.** A runaway workspace can't slow the others.
- **Simple operations.** Back up, export or delete a tenant by handling one file.

Start with one file, but keep `workspace_id` on everything so a later split is
mechanical.

---

## 8. Realtime fan-out

Server-sent events (SSE) push board changes to open tabs.

```
writer commits ──► hub.Publish(boardID, event)
                     │
                     ├─► subscriber chan (buffered 64) ──► SSE stream ──► browser
                     ├─► subscriber chan ...
                     └─► slow subscriber with a full buffer → disconnected
```

- **Hub:** `map[boardID]map[*sub]struct{}` behind a `sync.RWMutex`. Each subscriber
  has a **buffered** channel.
- **Never block the publisher.** If a subscriber's buffer is full, drop that
  subscriber; it reconnects and catches up. One slow phone on a bad connection must
  never stall the writer.
- **Resume with `Last-Event-ID`.** Send `id: <seq>` with every event. On reconnect,
  the browser sends the last ID, and the server replays from the `events` table. No
  updates are lost.
- **Cost:** each connection is one goroutine plus a buffer, a few KB. Ten thousand
  open boards cost tens of MB.
- **Heartbeat:** send a comment line (`: ping`) every 15–30 s so proxies (nginx)
  don't close idle connections. In nginx, set `proxy_buffering off` and
  `proxy_read_timeout` above the heartbeat interval.

Choose WebSockets instead only if clients need to send a high rate of messages over
the same connection. Otherwise SSE is simpler: it's plain HTTP and reconnects on
its own.

---

## 9. Backpressure and load shedding

A system without limits doesn't slow down gracefully. It queues until it runs out of
memory, then falls over. Put a bound on everything:

| Layer | Bound | When it's exceeded |
|---|---|---|
| HTTP server | `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` | Connection closed |
| Request | `context.WithTimeout` (e.g. 2 s) on every handler | `503` |
| In-flight requests | A semaphore (buffered channel) sized from Little's Law | `503 Service Unavailable` + `Retry-After` |
| Write queue | Bounded `reqs` channel | Caller's context expires → `503` |
| Per user | Token bucket (`golang.org/x/time/rate`) | `429 Too Many Requests` |
| SSE subscriber | Buffered channel | Disconnect (client resumes) |
| Request body | `http.MaxBytesReader` | `413` |

**Fail fast, early and cheap.** Rejecting a request at the door costs microseconds.
Accepting it, queuing it for 30 s and then timing out costs memory, a goroutine and
an angry user, and starves requests that could have succeeded.

**Idempotency keys.** Clients retry, so give each mutating request a client-generated
`Idempotency-Key` and store it with the write. A retried request is then detected
instead of applied twice. This matters most with the timeout case in section 5's
`Do`.

---

## 10. Durability and backups

| Tool | What it gives you |
|---|---|
| **Litestream** | Streams WAL changes continuously to S3-compatible storage or a local path. Point-in-time restore with a recovery point of seconds. Runs as a sidecar process; no app changes. |
| `VACUUM INTO '/backup/kanban-YYYYMMDD.db'` | A consistent snapshot while the app keeps running. Good for nightly copies. |
| `sqlite3 kanban.db .backup` | Online backup API, same idea. |

**Never `cp` a live database file.** You can copy it mid-write and get a corrupt
backup.

Define two targets:

- **RPO** (recovery point objective): how much data you can afford to lose. Litestream
  gets this down to seconds.
- **RTO** (recovery time objective): how long you can be down. A restore here means
  downloading one file and starting the binary, so minutes.

**Test restores.** A backup you have never restored from is a hope, not a backup.

High availability (a hot standby that takes over automatically) is where single-server
designs get more complex. LiteFS and rqlite (section 12) exist for that. Most internal
tools accept a few minutes of RTO in exchange for not running a cluster.

---

## 11. Measuring

### Tools

| Tool | Use |
|---|---|
| `hey`, `oha` | Quick fixed-rate HTTP load |
| `k6` | Scripted scenarios (log in → open board → drag card), ramps, thresholds |
| `net/http/pprof` | CPU, heap, goroutine and mutex profiles from the running binary |
| `go test -bench` | Microbenchmarks for the store layer |
| `runtime/metrics`, Prometheus | GC pauses, heap, goroutine count in production |

### Method

1. **Write a realistic mix.** Kanban traffic is read-heavy: perhaps 90% board loads,
   8% card moves and edits, and 2% creates. Benchmark that mix, not a single
   endpoint.
2. **Use an open-model load generator.** Send requests at a fixed **arrival rate**,
   not "N users each waiting for a response before the next request." Closed-loop
   tools hide latency spikes: when the server stalls, they stop sending, so the stall
   never appears in the numbers. This is *coordinated omission*, from Gil Tene's "How
   NOT to Measure Latency." Use `k6`'s `constant-arrival-rate` executor.
3. **Ramp until it breaks.** Find the rate where p99 crosses your service level
   objective (e.g. 100 ms). That rate is your capacity, and section 1 says to plan
   for 50–70% of it.
4. **Profile at the breaking point.** `go tool pprof http://localhost:6060/debug/pprof/profile`.
   Fix the top item. Repeat.
5. **Use realistic data volumes.** A board with 10 cards and one with 10,000 behave
   differently. Seed accordingly.

### What to watch

- Request rate, error rate, p50/p99/p99.9 latency, per endpoint
- Writer batch size (a histogram) and write-queue depth
- Size of the `-wal` file
- Goroutine count (a steady climb means a leak)
- GC pause and heap against `GOMEMLIMIT`
- SSE subscriber count and drop rate

---

## 12. The scaling ladder

Climb only when a measurement says the current step is exhausted. Each step adds
operational cost.

| Step | Do this | Signal to move up |
|---|---|---|
| **0. Tune** | Indexes, WAL, pragmas, group commit, one query per screen, ETags | Profiles show nothing left to fix |
| **1. Bigger box** | More RAM (working set in cache), more cores (read pool), enterprise NVMe (fast fsync) | The writer is saturated even with batching |
| **2. Split by tenant** | One SQLite file per workspace (section 7) | One tenant alone saturates a writer, or you need HA |
| **3. Replicas / HA** | LiteFS (read replicas, failover) or rqlite (Raft-replicated SQLite) | Write volume on a single dataset exceeds one SQLite writer |
| **4. Postgres** | Multiple concurrent writers, row-level locking. Keep it on the same box at first. The store interface makes the swap mechanical. | One Postgres primary is saturated |
| **5. Distributed** | Sharded Postgres (Citus), CockroachDB, or similar | Real global-scale write load. Few apps ever get here. |

**Keep the escape hatch open from day one:**

- Every database call goes through a `store` interface. No SQL in handlers.
- Every row carries a `workspace_id`.
- No SQLite-only tricks in the hot path that Postgres can't do (or isolate them
  behind the interface).

---

## 13. Anti-patterns

| Anti-pattern | Why it hurts |
|---|---|
| Opening a write transaction, then calling an external API | Holds the only write lock for hundreds of ms and stalls every writer |
| `SetMaxOpenConns(10)` on the **write** pool | Connections fight over one lock and `SQLITE_BUSY` storms follow. Use exactly one writer. |
| `SELECT *` + filtering in Go | Moves data that never needed to leave SQLite |
| One query per card (N+1) | 500 queries to render one board |
| Unbounded channels, goroutine-per-event without limits | Memory grows until the process is OOM-killed |
| Reporting mean latency | Hides the p99 your users actually feel |
| Benchmarking a closed loop | Coordinated omission makes a server that stalls look healthy |
| Adding Redis "for speed" before profiling | A network hop to cache something that was a 5 µs in-process lookup |
| `cp kanban.db` as a backup | Torn, possibly corrupt copies |
| Microservices on day one | Turns function calls into network calls, adding latency, failure modes and coordination |

---

## 14. Further reading

- **Expensify:** "Scaling SQLite to 4M QPS on a Single Server" (2018)
- **Tailscale:** "An Unlikely Database Migration" (David Crawshaw)
- **Let's Encrypt:** "The Next Gen Database Servers Powering Let's Encrypt" (2021)
- **SQLite docs:** [Write-Ahead Logging](https://www.sqlite.org/wal.html),
  [PRAGMA reference](https://www.sqlite.org/pragma.html),
  [When to use SQLite](https://www.sqlite.org/whentouse.html)
- **Litestream:** [litestream.io](https://litestream.io)
- **Gil Tene:** "How NOT to Measure Latency" (talk) — coordinated omission
- **Neil Gunther:** *Guerrilla Capacity Planning* — the Universal Scalability Law
- **Martin Kleppmann:** *Designing Data-Intensive Applications* — replication,
  partitioning, and when you actually need them
- **Jeff Dean:** "Latency Numbers Every Programmer Should Know"
