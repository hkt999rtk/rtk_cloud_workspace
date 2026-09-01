package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webAssets embed.FS

type dashboardData interface {
	snapshot() Snapshot
	detail(context.Context, string, string, int64) (RunDetail, error)
}

func newHandler(data dashboardData) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, data.snapshot())
	})
	mux.HandleFunc("GET /api/runs/{owner}/{repo}/{runID}", func(w http.ResponseWriter, r *http.Request) {
		owner, repo := r.PathValue("owner"), r.PathValue("repo")
		if !safeSegment(owner) || !safeSegment(repo) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid repository path"})
			return
		}
		runID, err := strconv.ParseInt(r.PathValue("runID"), 10, 64)
		if err != nil || runID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run id"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		detail, err := data.detail(ctx, owner, repo, runID)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errRunNotFound) || strings.Contains(err.Error(), "not found in dashboard") {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]string{"error": sanitizeError(err)})
			return
		}
		writeJSON(w, http.StatusOK, detail)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	content, _ := fs.Sub(webAssets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(content)))
	return securityHeaders(mux)
}

var errRunNotFound = errors.New("run not found")

func safeSegment(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://avatars.githubusercontent.com data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
