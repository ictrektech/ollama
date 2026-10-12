package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ollama/ollama/envconfig"
)

// Preserve Model Hub's optional preload and shared task-file contract without Python.
func preloadModelHub(ctx context.Context) {
	models := strings.TrimSpace(os.Getenv("OLLAMA_PRELOAD_MODELS"))
	if models == "" {
		models = os.Getenv("OLLAMA_PRELOAD_MODEL")
	}
	if models == "" {
		return
	}
	enabled := strings.Split(os.Getenv("OLLAMA_PRELOAD_ENABLED"), ",")
	host := envconfig.Host()
	if host.Hostname() == "0.0.0.0" || host.Hostname() == "::" {
		host.Host = net.JoinHostPort("127.0.0.1", host.Port())
	}
	base := host.String()
	wait, _ := strconv.Atoi(os.Getenv("OLLAMA_READY_WAIT_SECONDS"))
	if wait < 1 {
		wait = 300
	}
	client := &http.Client{Timeout: 24 * time.Hour}
	for i := 0; i < wait; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/version", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	mode := os.Getenv("OLLAMA_PRELOAD_MODE")
	keep := os.Getenv("OLLAMA_KEEP_ALIVE")
	if keep == "" {
		keep = "-1m"
	}
	dir := os.Getenv("MODELS_BASE_DIR")
	if dir == "" {
		dir = "/data"
	}
	dir = filepath.Join(dir, "shared", "tasks")
	for i, model := range strings.Split(models, ",") {
		model = strings.TrimSpace(strings.TrimPrefix(model, "ollama://"))
		if model == "" {
			continue
		}
		flag := strings.ToLower(strings.TrimSpace(enabled[min(i, len(enabled)-1)]))
		if flag != "true" && flag != "1" && flag != "yes" && flag != "on" {
			continue
		}
		if err := preloadOne(ctx, client, base, model, mode, keep, dir); err != nil {
			slog.Warn("Ollama preload failed", "model", model, "error", err)
		}
	}
}

func preloadPost(ctx context.Context, client *http.Client, base, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}
	return resp, nil
}

func preloadOne(ctx context.Context, client *http.Client, base, model, mode, keep, dir string) (err error) {
	id := "preload-" + uuid.NewString()
	canonical := model
	if !strings.Contains(model[strings.LastIndex(model, "/")+1:], ":") {
		canonical += ":latest"
	}
	task := map[string]any{"task_id": id, "model_id": "ollama://" + canonical, "phase": "PENDING", "progress": 0.0, "speed": nil, "eta": nil, "error_msg": nil, "files": nil}
	write := func(phase string, progress float64) {
		task["phase"] = phase
		task["progress"] = progress
		data, e := json.Marshal(task)
		if e == nil {
			e = os.MkdirAll(dir, 0755)
		}
		if e == nil {
			e = os.WriteFile(filepath.Join(dir, id+".json.tmp"), data, 0644)
		}
		if e == nil {
			e = os.Rename(filepath.Join(dir, id+".json.tmp"), filepath.Join(dir, id+".json"))
		}
		if e != nil {
			slog.Warn("failed to write Model Hub preload task", "error", e)
		}
	}
	write("PENDING", 0)
	defer func() {
		if err != nil {
			task["error_msg"] = err.Error()
			write("FAILED", 0)
		}
	}()
	resp, showErr := preloadPost(ctx, client, base, "/api/show", map[string]any{"model": model})
	if showErr == nil {
		resp.Body.Close()
	} else {
		resp, err = preloadPost(ctx, client, base, "/api/pull", map[string]any{"model": model, "stream": true})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		decoder := json.NewDecoder(resp.Body)
		last := int64(0)
		lastTime := time.Now()
		lastDigest := ""
		success := false
		for {
			var progress struct {
				Status, Digest, Error string
				Total, Completed      int64
			}
			if e := decoder.Decode(&progress); e == io.EOF {
				break
			} else if e != nil {
				return e
			}
			if progress.Error != "" {
				return fmt.Errorf("%s", progress.Error)
			}
			if progress.Status == "success" {
				success = true
				break
			}
			fraction := 0.0
			if progress.Total > 0 {
				fraction = min(1, float64(progress.Completed)/float64(progress.Total))
			}
			if progress.Digest != lastDigest {
				last = 0
				lastTime = time.Now()
				lastDigest = progress.Digest
			}
			now := time.Now()
			speed := float64(max(0, progress.Completed-last)) / max(0.001, now.Sub(lastTime).Seconds())
			if speed > 0 {
				task["speed"] = speed
				task["eta"] = float64(max(0, progress.Total-progress.Completed)) / speed
			}
			last = progress.Completed
			lastTime = now
			write("DOWNLOADING", fraction)
		}
		if !success {
			return fmt.Errorf("pull stream ended without success")
		}
		write("VERIFYING", 1)
	}
	write("READY", 1)
	path := "/api/generate"
	payload := map[string]any{"model": model, "prompt": "model-hub warmup", "stream": false, "keep_alive": keep}
	if mode == "embedding" {
		path = "/api/embed"
		payload = map[string]any{"model": model, "input": "model-hub warmup", "keep_alive": keep}
	}
	if mode == "rerank" {
		path = "/v1/rerank"
		payload = map[string]any{"model": model, "query": "model-hub warmup", "documents": []string{"model-hub warmup"}, "keep_alive": keep}
	}
	resp, err = preloadPost(ctx, client, base, path, payload)
	if err != nil {
		slog.Warn("Ollama model downloaded but warmup failed", "model", model, "error", err)
		err = nil
		return nil
	}
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return err
}
