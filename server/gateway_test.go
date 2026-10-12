package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/llm"
)

func TestTaskIDAndMetricsCleanup(t *testing.T) {
	s := &Server{gatewayMetrics: newGatewayTracker()}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/chat", nil)
	c.Request.Header.Set("X-Task-Id", "same-id")
	end1 := s.beginInference(c, "m")
	d, _ := gin.CreateTestContext(httptest.NewRecorder())
	d.Request = c.Request.Clone(context.Background())
	end2 := s.beginInference(d, "m")
	s.observeInference(c, "hello", 0, 0, 0, 0)
	s.observeInference(c, " world", 10, 5, time.Second, time.Second)
	snapshot := s.gatewayMetrics.snapshot()
	if snapshot["active_slots"] != 2 || snapshot["phase"] != "decode" {
		t.Fatalf("snapshot=%v", snapshot)
	}
	end1()
	end2()
	if s.gatewayMetrics.snapshot()["active_slots"] != 0 {
		t.Fatal("leaked slot")
	}
	r := gin.New()
	r.Use(taskIDMiddleware())
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	for _, id := range []string{"", "caller-id"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Task-Id", id)
		r.ServeHTTP(w, req)
		got := w.Header().Get("X-Task-Id")
		if got == "" || (id != "" && got != id) {
			t.Fatalf("task id=%q", got)
		}
	}
}

type rerankTestScorer struct{ cancel bool }

func (s rerankTestScorer) Score(ctx context.Context, req llm.ScoreRequest) (llm.ScoreResponse, error) {
	if s.cancel {
		return llm.ScoreResponse{}, ctx.Err()
	}
	if !strings.Contains(req.Rows[0].Prompt, "<think>\n\n</think>") {
		panic("incorrect trained prompt")
	}
	return llm.ScoreResponse{Logits: [][]float32{{2, 0}, {0, 2}}, InputTokens: 20}, nil
}
func TestQwenRerankAndOrdering(t *testing.T) {
	req := rerankRequest{Query: "q", Documents: []string{"low", "high"}, ReturnDocuments: true}
	n := 1
	req.TopN = &n
	scores, tokens, err := scoreQwenRerank(t.Context(), rerankTestScorer{}, req, 2048)
	if err != nil || tokens != 20 {
		t.Fatalf("tokens=%d err=%v", tokens, err)
	}
	results := rankResults(req, scores)
	if len(results) != 1 || results[0].Index != 1 || results[0].RelevanceScore < 0.8 || results[0].Document.Text != "high" {
		t.Fatalf("results=%v", results)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := scoreQwenRerank(ctx, rerankTestScorer{cancel: true}, req, 2048); err == nil {
		t.Fatal("cancel ignored")
	}
}
func TestRerankRoutesRejectInvalidInput(t *testing.T) {
	s := &Server{}
	h, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/rerank", "/v1/rerank", "/api/rerank"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"test","query":"q","documents":[]}`))
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, r)
		if w.Code != 400 || w.Header().Get("X-Task-Id") == "" {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["phase"] != "idle" {
		t.Fatalf("metrics=%s", w.Body.String())
	}
}

func TestLegacyRankHeadIdentification(t *testing.T) {
	m := &Model{metadata: ggufMetadata{KV: map[string]any{"general.architecture": "bert"}, ClassifierOutputShape: []uint64{1024}}}
	if !supportsRerank(m) {
		t.Fatal("legacy scalar classifier was not recognized")
	}
	m.metadata.ClassifierOutputShape = nil
	if supportsRerank(m) {
		t.Fatal("ordinary BERT embedding accepted as reranker")
	}
}
