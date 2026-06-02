package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AIClient interface {
	Embed(context.Context, string) ([]float64, bool)
	EmbedMany(context.Context, []string) [][]float64
	Generate(context.Context, string) (string, bool)
	EmbeddingsEnabled() bool
}

type OpenAIClient struct {
	BaseURL        string
	APIKey         string
	EmbeddingModel string
	AnswerModel    string
	HTTPClient     *http.Client
	EnableEmbed    bool
	EnableAnswer   bool
}

func NewOpenAIClient() *OpenAIClient {
	loadEnvFile(filepath.Join(homeDir(), ".env"))
	enableEmbeddings := os.Getenv("RTK_RAG_ENABLE_EMBEDDINGS") != "0"
	enableAnswers := os.Getenv("RTK_RAG_ENABLE_ANSWERS") != "0"
	baseURL := strings.TrimRight(envDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/")
	return &OpenAIClient{
		BaseURL:        baseURL,
		APIKey:         os.Getenv("OPENAI_API_KEY"),
		EmbeddingModel: envDefault("OPENAI_RAG_EMBEDDING_MODEL", "text-embedding-3-small"),
		AnswerModel:    envDefault("OPENAI_RAG_ANSWER_MODEL", "gpt-4.1-mini"),
		HTTPClient:     &http.Client{Timeout: 45 * time.Second},
		EnableEmbed:    enableEmbeddings,
		EnableAnswer:   enableAnswers,
	}
}

func (c *OpenAIClient) EmbeddingsEnabled() bool {
	return c.EnableEmbed && c.APIKey != ""
}

func (c *OpenAIClient) Embed(ctx context.Context, text string) ([]float64, bool) {
	rows := c.EmbedMany(ctx, []string{text})
	if len(rows) == 0 || rows[0] == nil {
		return nil, false
	}
	return rows[0], true
}

func (c *OpenAIClient) EmbedMany(ctx context.Context, texts []string) [][]float64 {
	out := make([][]float64, len(texts))
	if !c.EnableEmbed || c.APIKey == "" || len(texts) == 0 {
		return out
	}
	payload := map[string]any{"model": c.EmbeddingModel, "input": texts}
	var response struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "/embeddings", payload, &response); err != nil {
		return out
	}
	for _, row := range response.Data {
		if row.Index >= 0 && row.Index < len(out) {
			out[row.Index] = row.Embedding
		}
	}
	return out
}

func (c *OpenAIClient) Generate(ctx context.Context, prompt string) (string, bool) {
	if !c.EnableAnswer || c.APIKey == "" {
		return "", false
	}
	payload := map[string]any{"model": c.AnswerModel, "input": prompt, "temperature": 0.2}
	var response map[string]any
	if err := c.postJSON(ctx, "/responses", payload, &response); err != nil {
		return "", false
	}
	text := extractResponseText(response)
	return text, text != ""
}

func (c *OpenAIClient) postJSON(ctx context.Context, path string, payload any, target any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func extractResponseText(data map[string]any) string {
	if text, ok := data["output_text"].(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	var parts []string
	output, _ := data["output"].([]any)
	for _, item := range output {
		obj, _ := item.(map[string]any)
		content, _ := obj["content"].([]any)
		for _, entry := range content {
			entryObj, _ := entry.(map[string]any)
			if text, ok := entryObj["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func loadEnvFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
