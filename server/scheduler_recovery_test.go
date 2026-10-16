package server

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStopProofNeedsSchedulerBarrierAndNoLoadedRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := InitScheduler(ctx)
	model := &Model{ModelPath: "fixture-model"}
	deadline, done := context.WithTimeout(ctx, 20*time.Millisecond)
	if s.confirmedStopped(deadline, model) {
		t.Fatal("missing scheduler barrier confirmed stop")
	}
	done()
	go s.processPending(ctx)
	deadline, done = context.WithTimeout(ctx, time.Second)
	defer done()
	if !s.confirmedStopped(deadline, model) {
		t.Fatal("idle native scheduler not confirmed")
	}
	s.stopProofFailure.Store(true)
	if s.confirmedStopped(deadline, model) {
		t.Fatal("failed native process exit was treated as stopped")
	}
	s.stopProofFailure.Store(false)
	s.loadedMu.Lock()
	s.loaded[schedulerModelKey(model)] = &runnerRef{}
	s.loadedMu.Unlock()
	if s.confirmedStopped(deadline, model) {
		t.Fatal("loaded native runner reported stopped")
	}
}

func TestStopProofRefusesActiveInference(t *testing.T) {
	s := &Server{gatewayMetrics: newGatewayTracker()}
	s.gatewayMetrics.active["live"] = &gatewayRequest{ID: "live", Model: "fixture"}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/scheduler/confirm-stopped", strings.NewReader(`{"model":"fixture","nonce":"0123456789abcdef0123456789abcdef"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.SchedulerConfirmStoppedHandler(c)
	if w.Code != http.StatusConflict || len(s.gatewayMetrics.active) != 1 {
		t.Fatalf("active inference was affected: %d", w.Code)
	}
}
