.PHONY: run test lint build build-linux e2e

run: ## Dev server with static files at http://127.0.0.1:6220
	go run ./cmd/kanban -static ./web

test:
	go test -race ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)

build:
	go build -o bin/kanban ./cmd/kanban

build-linux: ## Static Linux binary, buildable from macOS (pure-Go SQLite, no cgo)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/kanban-linux-amd64 ./cmd/kanban

e2e: ## Browser smoke test against a running `make run` on a fresh data dir; needs Playwright
	mkdir -p bin/shots && node scripts/e2e.js bin/shots
