package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"local-rag/internal/rag"
)

func main() {
	os.Exit(run())
}

func run() int {
	workspace := flag.String("workspace", envDefault("RTK_RAG_WORKSPACE", mustGetwd()), "workspace path")
	dbPath := flag.String("db", os.Getenv("RTK_RAG_DB"), "SQLite database path")
	host := flag.String("host", envDefault("RTK_RAG_HOST", "127.0.0.1"), "listen host")
	port := flag.Int("port", envIntDefault("RTK_RAG_PORT", 8765), "listen port")
	watch := flag.Bool("watch", true, "watch by polling changed index")
	watchInterval := flag.Duration("watch-interval", 5*time.Second, "watch interval")
	skipInitialIndex := flag.Bool("skip-initial-index", false, "serve existing DB without startup full scan")
	flag.Parse()

	staticDir, err := rag.StaticDirFromCWD()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	index := rag.NewIndex(*workspace, *dbPath, rag.NewOpenAIClient())
	if !*skipInitialIndex {
		if _, err := index.IndexFull(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", *host, *port),
		Handler:           rag.NewServer(index, staticDir).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	watcher := &rag.Watcher{Index: index, Interval: *watchInterval}
	if *watch {
		watcher.Start()
	}
	errs := make(chan error, 1)
	go func() {
		log.Printf("Local RAG server listening on http://%s:%d", *host, *port)
		log.Printf("Workspace: %s", filepath.Clean(*workspace))
		errs <- server.ListenAndServe()
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		watcher.Stop()
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case <-signals:
		watcher.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
	return 0
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envIntDefault(key string, fallback int) int {
	var value int
	if raw := os.Getenv(key); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &value); err == nil {
			return value
		}
	}
	return fallback
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return wd
	}
	return abs
}
