package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	var workspace, address, apiURL string
	var interval time.Duration
	flag.StringVar(&workspace, "workspace", "", "workspace root containing .gitmodules (auto-detected by default)")
	flag.StringVar(&address, "address", "127.0.0.1:8787", "HTTP listen address")
	flag.StringVar(&apiURL, "github-api", "https://api.github.com", "GitHub API base URL")
	flag.DurationVar(&interval, "poll-interval", time.Minute, "GitHub polling interval")
	flag.Parse()

	if interval < time.Second {
		log.Fatal("poll interval must be at least 1s")
	}
	if workspace == "" {
		var err error
		workspace, err = findWorkspace()
		if err != nil {
			log.Fatal(err)
		}
	}
	repos, err := discoverRepositories(workspace, "hkt999rtk")
	if err != nil {
		log.Fatal(err)
	}
	token, err := githubToken()
	if err != nil {
		log.Fatal(err)
	}
	client := &githubClient{baseURL: apiURL, token: token, http: &http.Client{Timeout: 20 * time.Second}}
	poller := newPoller(client, repos, interval)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go poller.run(ctx)

	server := &http.Server{Addr: address, Handler: newHandler(poller), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("CI Flight Deck watching %d repositories at http://%s", len(repos), address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func findWorkspace() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".gitmodules")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find workspace .gitmodules; pass -workspace")
		}
		dir = parent
	}
}

func githubToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("GITHUB_TOKEN is unset and `gh auth token` failed: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("GitHub token is empty")
	}
	return token, nil
}
