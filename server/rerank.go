package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
)

type rerankRequest struct {
	Model           string          `json:"model"`
	Query           string          `json:"query"`
	Documents       rerankDocuments `json:"documents"`
	TopN            *int            `json:"top_n,omitempty"`
	ReturnDocuments bool            `json:"return_documents,omitempty"`
	Instruction     string          `json:"instruction,omitempty"`
	Options         map[string]any  `json:"options,omitempty"`
	KeepAlive       *api.Duration   `json:"keep_alive,omitempty"`
}
type rerankDocuments []string

func (d *rerankDocuments) UnmarshalJSON(data []byte) error {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	for _, raw := range rows {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			var obj struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(raw, &obj); err != nil || obj.Text == nil {
				return fmt.Errorf("documents must contain strings or objects with text")
			}
			text = *obj.Text
		}
		*d = append(*d, text)
	}
	return nil
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       *struct {
		Text string `json:"text"`
	} `json:"document,omitempty"`
}

// Qwen3's trained reranker prompt and yes/no softmax, rather than generated text.
func qwenRerankPrompt(instruction, query, document string) string {
	if instruction == "" {
		instruction = "Given a web search query, retrieve relevant passages that answer the query"
	}
	return "<|im_start|>system\nJudge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be \"yes\" or \"no\".<|im_end|>\n<|im_start|>user\n<Instruct>: " + instruction + "\n<Query>: " + query + "\n<Document>: " + document + "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

func scoreQwenRerank(ctx context.Context, scorer llm.Scorer, req rerankRequest, numCtx int) ([]float64, int, error) {
	scores := make([]float64, 0, len(req.Documents))
	tokens := 0
	for start := 0; start < len(req.Documents); start += 64 {
		input := llm.ScoreRequest{MaxTokens: numCtx}
		for _, doc := range req.Documents[start:min(start+64, len(req.Documents))] {
			input.Rows = append(input.Rows, llm.ScoreRow{Prompt: qwenRerankPrompt(req.Instruction, req.Query, doc), Candidates: []string{"no", "yes"}})
		}
		result, err := scorer.Score(ctx, input)
		if err != nil {
			return nil, 0, err
		}
		if len(result.Logits) != len(input.Rows) {
			return nil, 0, fmt.Errorf("reranker returned incorrect score count")
		}
		for _, row := range result.Logits {
			if len(row) != 2 {
				return nil, 0, fmt.Errorf("reranker requires yes/no logits")
			}
			d := float64(row[0]) - float64(row[1])
			if math.IsNaN(d) || math.IsInf(d, 0) {
				return nil, 0, fmt.Errorf("invalid reranker logits")
			}
			scores = append(scores, 1/(1+math.Exp(d)))
		}
		tokens += result.InputTokens
	}
	return scores, tokens, nil
}

func rankResults(req rerankRequest, scores []float64) []rerankResult {
	results := make([]rerankResult, len(scores))
	for i, score := range scores {
		results[i] = rerankResult{Index: i, RelevanceScore: score}
		if req.ReturnDocuments {
			results[i].Document = &struct {
				Text string `json:"text"`
			}{req.Documents[i]}
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].RelevanceScore > results[j].RelevanceScore })
	if req.TopN != nil {
		results = results[:min(*req.TopN, len(results))]
	}
	return results
}

func isQwenReranker(m *Model) bool {
	return m.metadata.String("general.architecture") == "qwen3" && strings.Contains(strings.ToLower(m.Name+" "+m.metadata.String("general.name")), "rerank")
}
func hasRankHead(m *Model) bool {
	shape := m.metadata.ClassifierOutputShape
	arch := m.metadata.String("general.architecture")
	return m.metadata.Int("pooling_type") == 4 || ((arch == "bert" || arch == "modernbert" || arch == "xlmroberta") && (len(shape) == 1 || (len(shape) == 2 && shape[1] == 1)))
}
func supportsRerank(m *Model) bool { return hasRankHead(m) || isQwenReranker(m) }

func (s *Server) RerankHandler(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<20)
	var req rerankRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Query) == "" || len(req.Documents) == 0 || (req.TopN != nil && *req.TopN < 1) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model, query and documents are required; top_n must be positive"})
		return
	}
	defer s.beginInference(c, req.Model)()
	ref, err := parseAndValidateModelRef(req.Model)
	if err != nil || ref.Source == modelSourceCloud {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rerank requires a local model"})
		return
	}
	name, err := getExistingName(ref.Name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}
	m, err := GetModel(name.String())
	if err != nil {
		handleScheduleError(c, req.Model, err)
		return
	}
	qwen := isQwenReranker(m) && m.metadata.Int("pooling_type") != 4
	rankHead := hasRankHead(m)
	if !qwen && !rankHead {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model has no supported reranking head; use a rank-pooling GGUF or Qwen3-Reranker"})
		return
	}
	r, _, _, err := s.scheduleRunnerForModel(c.Request.Context(), m, nil, req.Options, req.KeepAlive, nil)
	if err != nil {
		handleScheduleError(c, req.Model, err)
		return
	}
	started := time.Now()
	var scores []float64
	var tokens int
	if qwen {
		scorer, ok := r.(llm.Scorer)
		if !ok {
			err = fmt.Errorf("runner does not support Qwen3 scoring")
		} else {
			scores, tokens, err = scoreQwenRerank(c.Request.Context(), scorer, req, r.ContextLength())
		}
	} else {
		reranker, ok := r.(llm.Reranker)
		if !ok {
			err = fmt.Errorf("runner does not support rank pooling")
		} else {
			scores, tokens, err = reranker.Rerank(c.Request.Context(), req.Query, req.Documents)
		}
	}
	if err != nil {
		s.sched.expireRunnersForRuntimeOOM(m, err)
		status := http.StatusInternalServerError
		var e api.StatusError
		if errors.As(err, &e) {
			status = e.StatusCode
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	s.observeInference(c, "", tokens, 0, time.Since(started), 0)
	c.JSON(http.StatusOK, gin.H{"model": req.Model, "object": "list", "results": rankResults(req, scores), "usage": gin.H{"prompt_tokens": tokens, "total_tokens": tokens}})
}
