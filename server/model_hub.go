package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func modelHubPayload() map[string]any {
	host, _ := os.Hostname()
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	id := get("MODEL_HUB_SERVICE_ID", host)
	base := get("MODEL_HUB_SERVICE_BASE_URL", "http://"+id+":11434")
	metadata := map[string]string{"container_name": get("HOSTNAME", id)}
	for key, env := range map[string]string{"models_base_dir": "MODELS_BASE_DIR", "models_host_dir": "MODELS_HOST_DIR", "ollama_models": "OLLAMA_MODELS", "num_parallel": "OLLAMA_NUM_PARALLEL", "context_length": "OLLAMA_CONTEXT_LENGTH"} {
		metadata[key] = get(env, "")
	}
	return map[string]any{"service_id": id, "name": get("MODEL_HUB_SERVICE_NAME", id), "kind": "ollama", "base_url": base, "gateway_url": get("MODEL_HUB_SERVICE_GATEWAY_URL", base), "role": get("MODEL_HUB_SERVICE_ROLE", "generate"), "api_contract": map[string]string{"metrics_path": "/metrics"}, "metadata": metadata}
}

func registerModelHub(ctx context.Context) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("MODEL_HUB_REGISTER_URL")), "/")
	if base == "" {
		return
	}
	body, _ := json.Marshal(modelHubPayload())
	attempts, _ := strconv.Atoi(os.Getenv("MODEL_HUB_REGISTER_ATTEMPTS"))
	if attempts < 1 {
		attempts = 60
	}
	interval, _ := strconv.ParseFloat(os.Getenv("MODEL_HUB_REGISTER_RETRY_INTERVAL"), 64)
	if interval <= 0 {
		interval = 5
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/inference-services/register", bytes.NewReader(body))
		if err != nil {
			slog.Warn("invalid Model Hub registration URL")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				slog.Info("registered Ollama to Model Hub")
				return
			}
		}
		if i+1 < attempts {
			timer := time.NewTimer(time.Duration(interval * float64(time.Second)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	slog.Warn("Model Hub registration failed after retries")
}
