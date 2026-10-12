package llm

import (
	"encoding/json"
	"golang.org/x/sync/semaphore"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeRerankScoresPreserveInputIndices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/rerank" {
			t.Errorf("path=%s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["query"] != "q" || body["top_n"] != float64(2) {
			t.Errorf("body=%v", body)
		}
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":2},{"index":0,"relevance_score":-2}],"usage":{"total_tokens":8}}`))
	}))
	defer server.Close()
	r := &llamaServerRunner{port: server.Listener.Addr().(*net.TCPAddr).Port, client: server.Client(), sem: semaphore.NewWeighted(1)}
	scores, tokens, err := r.Rerank(t.Context(), "q", []string{"low", "high"})
	if err != nil || tokens != 8 || len(scores) != 2 || scores[0] >= 0.2 || scores[1] <= 0.8 {
		t.Fatalf("scores=%v tokens=%d err=%v", scores, tokens, err)
	}
}
