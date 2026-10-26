// Command kanban serves the kanban API. In production nginx serves web/ and
// proxies /api/ here; with -static it serves web/ itself for development.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" // registers on http.DefaultServeMux, served only on -debug-addr
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iAccelTech/kanban/internal/api"
	"github.com/iAccelTech/kanban/internal/hub"
	"github.com/iAccelTech/kanban/internal/store"
)

func main() {
	addr := flag.String("addr", env("KANBAN_ADDR", "127.0.0.1:6220"), "listen address (keep it on loopback; nginx is the way in)")
	data := flag.String("data", env("KANBAN_DATA", "./data"), "data directory")
	static := flag.String("static", "", "serve static files from this directory (development only)")
	debugAddr := flag.String("debug-addr", env("KANBAN_DEBUG_ADDR", "127.0.0.1:6221"), "pprof listen address; empty disables it")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *addr, *data, *static, *debugAddr); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(log *slog.Logger, addr, data, static, debugAddr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	h := hub.New()
	st, err := store.Open(ctx, filepath.Join(data, "kanban.db"), h.Publish)
	if err != nil {
		return err
	}
	defer st.Close()

	mux := http.NewServeMux()
	apiHandler := api.New(st, h, log).Handler()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	if static != "" {
		files := http.FileServer(http.Dir(static))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		}))
		log.Info("serving static files", "dir", static)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second, // event streams clear this per request
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	srv.RegisterOnShutdown(h.Close) // end event streams so Shutdown doesn't wait on them

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", "http://"+ln.Addr().String())

	if debugAddr != "" {
		go func() {
			if err := http.ListenAndServe(debugAddr, http.DefaultServeMux); err != nil {
				log.Warn("debug listener stopped", "addr", debugAddr, "err", err)
			}
		}()
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
