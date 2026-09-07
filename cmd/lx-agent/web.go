package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mgnlia/lx-agent/internal/archive"
	"github.com/mgnlia/lx-agent/internal/web"
)

// handleWeb serves the archive as a browsable site.
//
// The listen address is the access control. Binding to the host's Tailscale
// address means the site is reachable from the tailnet and from nowhere else —
// not the LAN, not the public interface — which matters because this is a
// student's own coursework with no login in front of it.
func handleWeb(cfg config, logger *slog.Logger, args []string) {
	listen := "127.0.0.1:8788"
	dir := cfg.Archive.Dir

	for i := 0; i < len(args); i++ {
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}

		switch args[i] {
		case "--listen", "-listen", "--addr":
			v, ok := next()
			if !ok {
				exitErr(errors.New("--listen requires host:port"))
			}
			listen = v
		case "--dir", "--out":
			v, ok := next()
			if !ok {
				exitErr(errors.New("--dir requires a path"))
			}
			dir = v
		default:
			exitErr(fmt.Errorf("unknown web flag: %s", args[i]))
		}
	}

	if v := os.Getenv("LX_WEB_LISTEN"); v != "" && listen == "127.0.0.1:8788" {
		listen = v
	}

	resolved, err := archive.ExpandDir(dir)
	if err != nil {
		exitErr(err)
	}
	if _, err := os.Stat(resolved); err != nil {
		exitErr(fmt.Errorf("archive dir %s: %w", resolved, err))
	}

	store := web.NewStore(resolved)
	srv, err := web.New(store, logger)
	if err != nil {
		exitErr(err)
	}

	// Bind before announcing: a typo'd address should fail now, not look
	// healthy while serving nothing.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		exitErr(fmt.Errorf("listen on %s: %w", listen, err))
	}

	snap := store.Snapshot()
	logger.Info("serving archive",
		"addr", ln.Addr().String(), "dir", resolved,
		"courses", len(snap.Courses), "files", snap.Count)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		exitErr(err)
	}
}
