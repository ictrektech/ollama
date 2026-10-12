package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestModelHubPreloadTaskAndRerankWarmup(t *testing.T) {
	paths := []string{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/show":
			w.WriteHeader(404)
		case "/api/pull":
			_, _ = w.Write([]byte("{\"status\":\"downloading\",\"total\":10,\"completed\":5}\n{\"status\":\"success\"}\n"))
		case "/v1/rerank":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["query"] != "model-hub warmup" || body["keep_alive"] != "-1m" {
				t.Errorf("body=%v", body)
			}
			_, _ = w.Write([]byte(`{"results":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer h.Close()
	dir := t.TempDir()
	if err := preloadOne(t.Context(), h.Client(), h.URL, "bge-reranker", "rerank", "-1m", dir); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("paths=%v", paths)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("tasks=%v", files)
	}
	data, _ := os.ReadFile(files[0])
	var task map[string]any
	_ = json.Unmarshal(data, &task)
	if task["phase"] != "READY" || task["model_id"] != "ollama://bge-reranker:latest" || task["progress"] != float64(1) {
		t.Fatalf("task=%v", task)
	}
}
