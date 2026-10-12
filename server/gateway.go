package server

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func taskIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Task-Id"))
		if id == "" {
			id = uuid.NewString()
		}
		c.Header("X-Task-Id", id)
		c.Request.Header.Set("X-Task-Id", id)
		c.Next()
	}
}

type gatewayRequest struct {
	ID                     string  `json:"id"`
	TaskID                 string  `json:"task_id"`
	Model                  string  `json:"model"`
	Phase                  string  `json:"phase"`
	Elapsed                float64 `json:"elapsed_seconds"`
	PrefillTPS             float64 `json:"prefill_tokens_per_second"`
	DecodeTPS              float64 `json:"decode_tokens_per_second"`
	TPS                    float64 `json:"tokens_per_second"`
	PromptTokens           int     `json:"prompt_tokens"`
	CompletionTokens       int     `json:"completion_tokens"`
	Estimated              bool    `json:"estimated"`
	started, decodeStarted time.Time
	text                   strings.Builder
}

type gatewayTracker struct {
	mu     sync.Mutex
	active map[string]*gatewayRequest
}

func newGatewayTracker() *gatewayTracker {
	return &gatewayTracker{active: map[string]*gatewayRequest{}}
}

func (s *Server) beginInference(c *gin.Context, model string) func() {
	if s.gatewayMetrics == nil {
		return func() {}
	}
	id := uuid.NewString() // Separate from task ID: callers may reuse correlation IDs.
	s.gatewayMetrics.mu.Lock()
	s.gatewayMetrics.active[id] = &gatewayRequest{ID: id, TaskID: c.GetHeader("X-Task-Id"), Model: model, Phase: "prefill", started: time.Now()}
	s.gatewayMetrics.mu.Unlock()
	c.Set("gateway_request", id)
	return func() { s.gatewayMetrics.mu.Lock(); delete(s.gatewayMetrics.active, id); s.gatewayMetrics.mu.Unlock() }
}

// Estimate over accumulated text so chunk boundaries do not inflate counts.
func estimateGatewayTokens(text string) int {
	count, word := 0, false
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			count++
			word = false
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if !word {
				count++
			}
			word = true
		} else {
			word = false
			if !unicode.IsSpace(r) {
				count++
			}
		}
	}
	return count
}

func (s *Server) observeInference(c *gin.Context, text string, prompt, completion int, prefill, decode time.Duration) {
	if s.gatewayMetrics == nil {
		return
	}
	id := c.GetString("gateway_request")
	s.gatewayMetrics.mu.Lock()
	defer s.gatewayMetrics.mu.Unlock()
	r := s.gatewayMetrics.active[id]
	if r == nil {
		return
	}
	if text != "" {
		if r.Phase != "decode" {
			r.Phase = "decode"
			r.decodeStarted = time.Now()
		}
		r.text.WriteString(text)
		r.CompletionTokens = estimateGatewayTokens(r.text.String())
		r.Estimated = true
		r.DecodeTPS = float64(r.CompletionTokens) / math.Max(time.Since(r.decodeStarted).Seconds(), 0.001)
	}
	if prompt > 0 {
		r.PromptTokens = prompt
	}
	if prefill > 0 {
		r.PrefillTPS = float64(prompt) / prefill.Seconds()
	}
	if completion > 0 {
		r.CompletionTokens = completion
		r.Estimated = false
	}
	if decode > 0 {
		r.DecodeTPS = float64(completion) / decode.Seconds()
	}
	r.TPS = r.DecodeTPS
}

func gatewayParallel() int {
	n, _ := strconv.Atoi(os.Getenv("OLLAMA_NUM_PARALLEL"))
	if n < 1 {
		n = 1
	}
	return n
}

func roundGateway(v float64) float64 { return math.Round(v*100) / 100 }

func (t *gatewayTracker) snapshot() gin.H {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := gatewayParallel()
	byModel := map[string]int{}
	phases := map[string]map[string]int{}
	models := map[string]gin.H{}
	requests := make([]gatewayRequest, 0, len(t.active))
	phase := "idle"
	for _, item := range t.active {
		byModel[item.Model]++
		if phases[item.Model] == nil {
			phases[item.Model] = map[string]int{}
		}
		phases[item.Model][item.Phase]++
		m := models[item.Model]
		if m == nil {
			m = gin.H{"active_slots": 0, "total_slots": n, "phase": item.Phase, "prompt_tokens": 0, "completion_tokens": 0, "prefill_tokens_per_second": 0.0, "decode_tokens_per_second": 0.0, "tokens_per_second": 0.0}
			models[item.Model] = m
		}
		m["active_slots"] = byModel[item.Model]
		m["slot_usage"] = fmt.Sprintf("%d/%d", byModel[item.Model], n)
		m["phase_counts"] = phases[item.Model]
		m["prompt_tokens"] = m["prompt_tokens"].(int) + item.PromptTokens
		m["completion_tokens"] = m["completion_tokens"].(int) + item.CompletionTokens
		m["prefill_tokens_per_second"] = roundGateway(item.PrefillTPS)
		m["decode_tokens_per_second"] = roundGateway(item.DecodeTPS)
		m["tokens_per_second"] = roundGateway(item.TPS)
		if item.Phase == "decode" {
			phase = "decode"
			m["phase"] = "decode"
		} else if phase == "idle" {
			phase = item.Phase
		}
		// Copy only public fields; strings.Builder must never be copied after use.
		requests = append(requests, gatewayRequest{ID: item.ID, TaskID: item.TaskID, Model: item.Model, Phase: item.Phase, Elapsed: math.Round(time.Since(item.started).Seconds()*1000) / 1000, PrefillTPS: roundGateway(item.PrefillTPS), DecodeTPS: roundGateway(item.DecodeTPS), TPS: roundGateway(item.TPS), PromptTokens: item.PromptTokens, CompletionTokens: item.CompletionTokens, Estimated: item.Estimated})
	}
	return gin.H{"active_slots": len(t.active), "total_slots": n, "available_slots": max(0, n-len(t.active)), "slot_usage": fmt.Sprintf("%d/%d", len(t.active), n), "active_by_model": byModel, "phase": phase, "phase_by_model": phases, "model_metrics": models, "requests": requests}
}
