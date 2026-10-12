package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"

	"github.com/ollama/ollama/api"
)

// Reranker is implemented by runners with a trained cross-encoder rank head.
type Reranker interface {
	Rerank(context.Context, string, []string) ([]float64, int, error)
}

func (s *llamaServerRunner) Rerank(ctx context.Context, query string, documents []string) ([]float64, int, error) {
	if err := s.sem.Acquire(ctx, 1); err != nil {
		return nil, 0, err
	}
	defer s.sem.Release(1)
	body, err := json.Marshal(map[string]any{"query": query, "documents": documents, "top_n": len(documents), "normalize": true})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/rerank", s.port), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, api.StatusError{StatusCode: resp.StatusCode, ErrorMessage: string(data)}
	}
	var result struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}
	if len(result.Results) != len(documents) {
		return nil, 0, fmt.Errorf("reranker returned %d scores for %d documents", len(result.Results), len(documents))
	}
	scores := make([]float64, len(documents))
	seen := make([]bool, len(documents))
	for _, r := range result.Results {
		if r.Index < 0 || r.Index >= len(scores) || seen[r.Index] || math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			return nil, 0, fmt.Errorf("invalid reranker result")
		}
		scores[r.Index] = 1 / (1 + math.Exp(-r.Score))
		// Converted Qwen rank heads already emit a binary softmax probability.
		if s.metadata != nil && (s.metadata.KV().Architecture() == "qwen3" || s.metadata.KV().Architecture() == "qwen3vl") {
			scores[r.Index] = r.Score
		}
		seen[r.Index] = true
	}
	return scores, max(result.Usage.PromptTokens, result.Usage.TotalTokens), nil
}
