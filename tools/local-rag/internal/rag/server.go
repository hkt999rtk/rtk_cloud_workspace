package rag

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Index     *Index
	StaticDir string
	mu        sync.Mutex
}

func NewServer(index *Index, staticDir string) *Server {
	return &Server{Index: index, StaticDir: mustAbs(staticDir)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/query", s.handleQuery)
	mux.HandleFunc("/api/index/full", s.handleIndexFull)
	mux.HandleFunc("/api/index/changed", s.handleIndexChanged)
	mux.HandleFunc("/", s.handleStatic)
	return loggingMiddleware(mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	status, err := s.Index.Status()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var body struct {
		Query   string            `json:"query"`
		Filters map[string]string `json:"filters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if body.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
		return
	}
	response, err := s.Index.Query(r.Context(), body.Query, body.Filters, 8)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleIndexFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.Index.IndexFull(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleIndexChanged(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.Index.IndexChanged(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	path := r.URL.Path
	if path == "" || path == "/" {
		path = "/index.html"
	}
	target := filepath.Clean(filepath.Join(s.StaticDir, strings.TrimPrefix(path, "/")))
	if !strings.HasPrefix(target, s.StaticDir+string(filepath.Separator)) && target != s.StaticDir {
		target = filepath.Join(s.StaticDir, "index.html")
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		target = filepath.Join(s.StaticDir, "index.html")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(target))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "charset=") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		status = http.StatusInternalServerError
		raw = []byte(`{"error":"json_encode_failed"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.RemoteAddr, r.Method+" "+r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

type Watcher struct {
	Index    *Index
	Interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
}

func (w *Watcher) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		return
	}
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = w.Index.IndexChanged(context.Background())
			case <-w.stop:
				return
			}
		}
	}()
}

func (w *Watcher) Stop() {
	w.mu.Lock()
	stop := w.stop
	done := w.done
	w.stop = nil
	w.done = nil
	w.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func StaticDirFromCWD() (string, error) {
	candidates := []string{
		filepath.Join(".", "web"),
		filepath.Join("tools", "local-rag", "web"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return mustAbs(candidate), nil
		}
	}
	return "", errors.New("web static directory not found")
}
