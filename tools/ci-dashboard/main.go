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
	var workspace, address, apiURL, webhookSecretFile string
	var interval time.Duration
	flag.StringVar(&workspace, "workspace", "", "workspace root containing .gitmodules (auto-detected by default)")
	flag.StringVar(&address, "address", "127.0.0.1:8787", "HTTP listen address")
	flag.StringVar(&apiURL, "github-api", "https://api.github.com", "GitHub API base URL")
	flag.StringVar(&webhookSecretFile, "webhook-secret-file", defaultWebhookSecretFile(), "file containing the GitHub webhook secret")
	flag.DurationVar(&interval, "poll-interval", 20*time.Minute, "GitHub polling interval")
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
	webhookSecret, err := loadWebhookSecret(webhookSecretFile)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go poller.run(ctx)

	server := &http.Server{Addr: address, Handler: newHandler(poller, webhookSecret), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("CI Flight Deck watching %d repositories at http://%s", len(repos), address)
	if len(webhookSecret) == 0 {
		log.Printf("GitHub webhook refresh disabled: no secret at %s", webhookSecretFile)
	} else {
		log.Printf("GitHub webhook refresh enabled at /webhooks/github")
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func defaultWebhookSecretFile() string {
	if path := strings.TrimSpace(os.Getenv("RTK_CI_DASHBOARD_WEBHOOK_SECRET_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "rtk-ci-dashboard", "webhook-secret")
}

func loadWebhookSecret(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read webhook secret: %w", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) == 0 {
		return nil, errors.New("webhook secret file is empty")
	}
	return secret, nil
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
