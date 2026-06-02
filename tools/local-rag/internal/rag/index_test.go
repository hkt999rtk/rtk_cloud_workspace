package rag

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type noopAI struct{}

func (noopAI) Embed(context.Context, string) ([]float64, bool) { return nil, false }
func (noopAI) EmbedMany(context.Context, []string) [][]float64 { return nil }
func (noopAI) Generate(context.Context, string) (string, bool) { return "", false }
func (noopAI) EmbeddingsEnabled() bool                         { return false }

func makeIndex(workspace string) *Index {
	return NewIndex(workspace, filepath.Join(workspace, ".rag", "rag.db"), noopAI{})
}

func initGitRepo(t *testing.T, path string) string {
	t.Helper()
	run(t, path, "git", "init", "-q")
	run(t, path, "git", "config", "user.email", "test@example.com")
	run(t, path, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, path, "git", "add", "README.md")
	run(t, path, "git", "commit", "-q", "-m", "init")
	out := run(t, path, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

func makeWorkspace(t *testing.T) string {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, workspace)
	if err := os.Mkdir(filepath.Join(workspace, "repos"), 0755); err != nil {
		t.Fatal(err)
	}
	contracts := filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc")
	service := filepath.Join(workspace, "repos", "rtk_video_cloud")
	copied := filepath.Join(service, "docs", "rtk_cloud_contracts_doc")
	for _, path := range []string{contracts, service, copied} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	initGitRepo(t, contracts)
	initGitRepo(t, service)
	return workspace
}

func TestMarkdownChunkingPreservesHeadingAndLineMetadata(t *testing.T) {
	workspace := makeWorkspace(t)
	doc := filepath.Join(workspace, "repos", "rtk_video_cloud", "docs", "auth.md")
	if err := os.WriteFile(doc, []byte("# Auth\n\nIntro.\n\n## Device Certificate\n\nDevice obtains a certificate during activation.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	chunks, err := makeIndex(workspace).ChunkFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	var cert Chunk
	for _, chunk := range chunks {
		if strings.Contains(chunk.Content, "Device obtains") {
			cert = chunk
		}
	}
	if cert.Heading != "Auth > Device Certificate" {
		t.Fatalf("heading = %q", cert.Heading)
	}
	if cert.LineStart != 5 || cert.LineEnd != 7 {
		t.Fatalf("lines = %d-%d", cert.LineStart, cert.LineEnd)
	}
	if cert.FilePath != "repos/rtk_video_cloud/docs/auth.md" || cert.RepoName != "rtk_video_cloud" || cert.SourceLayer != "service" {
		t.Fatalf("bad metadata: %+v", cert)
	}
}

func TestYAMLOpenAPIIngestionAndClassification(t *testing.T) {
	workspace := makeWorkspace(t)
	api := filepath.Join(workspace, "repos", "rtk_video_cloud", "docs", "openapi.yaml")
	if err := os.WriteFile(api, []byte("openapi: 3.0.0\npaths:\n  /devices/{id}/activate:\n    post:\n      summary: Device activation\n"), 0644); err != nil {
		t.Fatal(err)
	}
	index := makeIndex(workspace)
	if _, err := index.IndexFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := index.Search(context.Background(), "device activation", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range results {
		found = found || strings.Contains(result.Content, "Device activation")
	}
	if !found {
		t.Fatalf("expected Device activation result, got %+v", results)
	}
	classification, layer := ClassifyDocument("repos/rtk_video_cloud/docs/openapi.yaml")
	if classification != "source" || layer != "service" {
		t.Fatalf("classification = %s/%s", classification, layer)
	}
}

func TestContractsSourceRanksAboveCopiedContractDocs(t *testing.T) {
	workspace := makeWorkspace(t)
	writeFile(t, filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc", "AUTH.md"), "# Auth\n\nDevice token is issued by canonical contract.\n")
	writeFile(t, filepath.Join(workspace, "repos", "rtk_video_cloud", "docs", "rtk_cloud_contracts_doc", "AUTH.md"), "# Auth\n\nDevice token is copied service documentation.\n")
	index := makeIndex(workspace)
	if _, err := index.IndexFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := index.Search(context.Background(), "device token auth contract", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].FilePath != "repos/rtk_cloud_contracts_doc/AUTH.md" {
		t.Fatalf("unexpected top result: %+v", results)
	}
}

func TestChangedIndexMarksDeletedChunksInactive(t *testing.T) {
	workspace := makeWorkspace(t)
	doc := filepath.Join(workspace, "docs", "runtime.md")
	writeFile(t, doc, "# Runtime\n\nVideo server uses API, storage, MQTT, and WebRTC.\n")
	index := makeIndex(workspace)
	if _, err := index.IndexFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := index.Search(context.Background(), "WebRTC storage MQTT", 1, nil)
	if err != nil || len(results) == 0 {
		t.Fatalf("expected result: %+v err=%v", results, err)
	}
	if err := os.Remove(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := index.IndexChanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", index.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active int
	if err := db.QueryRow("select active from documents where path = ?", "docs/runtime.md").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active = %d", active)
	}
	results, err = index.Search(context.Background(), "WebRTC storage MQTT", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results after delete, got %+v", results)
	}
}

func TestRepositoryStatusReportsDirtySubmoduleLikeRepo(t *testing.T) {
	workspace := makeWorkspace(t)
	repo := filepath.Join(workspace, "repos", "rtk_video_cloud")
	writeFile(t, filepath.Join(repo, "dirty.md"), "dirty\n")
	repos := DiscoverRepositories(workspace)
	var video RepositoryInfo
	for _, repo := range repos {
		if repo.Name == "rtk_video_cloud" {
			video = repo
		}
	}
	if !video.Dirty || video.Path != "repos/rtk_video_cloud" || len(video.CommitSHA) != 40 {
		t.Fatalf("bad repo info: %+v", video)
	}
}

func TestQueryReturnsAnswerCitationsAndConflictNotes(t *testing.T) {
	workspace := makeWorkspace(t)
	writeFile(t, filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc", "AUTH.md"), "# Auth\n\nDevices obtain credentials during activation using a signed device certificate.\n")
	writeFile(t, filepath.Join(workspace, "repos", "rtk_video_cloud", "docs", "auth.md"), "# Auth\n\nLegacy notes say devices use a bootstrap token before certificates.\n")
	index := makeIndex(workspace)
	if _, err := index.IndexFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := index.Query(context.Background(), "device 怎麼取得認證", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Answer, "直接答案") || len(response.Citations) == 0 || len(response.MatchedChunks) == 0 || len(response.ConfidenceNotes) == 0 || len(response.Conflicts) == 0 {
		t.Fatalf("bad query response: %+v", response)
	}
	if response.Citations[0].Path != "repos/rtk_cloud_contracts_doc/AUTH.md" {
		t.Fatalf("top citation = %+v", response.Citations[0])
	}
}

func TestServerStatusQueryAndReindexEndpoints(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, workspace)
	writeFile(t, filepath.Join(workspace, "docs", "auth.md"), "# Auth\n\nDevice activation issues a certificate and token.\n")
	index := makeIndex(workspace)
	if _, err := index.IndexFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	staticDir := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(staticDir, "index.html"), "<html></html>")
	server := httptest.NewServer(NewServer(index, staticDir).Routes())
	defer server.Close()

	var status StatusResponse
	getJSON(t, server.URL+"/api/status", &status)
	if status.ActiveDocuments < 1 {
		t.Fatalf("status = %+v", status)
	}
	var query QueryResponse
	postJSON(t, server.URL+"/api/query", map[string]string{"query": "device 認證"}, &query)
	if !strings.Contains(query.Answer, "直接答案") || len(query.Citations) == 0 {
		t.Fatalf("query = %+v", query)
	}
	var changed IndexResult
	postJSON(t, server.URL+"/api/index/changed", map[string]string{}, &changed)
	if changed.ActiveFiles == 0 {
		t.Fatalf("changed = %+v", changed)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, stderr.String())
	}
	return out.String()
}

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, url string, payload any, target any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
