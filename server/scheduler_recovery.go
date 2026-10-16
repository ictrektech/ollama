package server

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

// A model unload is already part of the public native API. This endpoint adds
// a native loading/process-exit barrier for the owning scheduler to reconcile
// an interrupted request; it refuses to stop an active HTTP inference.
func (s *Server) SchedulerConfirmStoppedHandler(c *gin.Context) {
	var body struct {
		Model string `json:"model"`
		Nonce string `json:"nonce"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Model == "" || len(body.Nonce) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model and nonce required"})
		return
	}
	if s.gatewayMetrics == nil || s.gatewayMetrics.snapshot()["active_slots"].(int) != 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "inference still active"})
		return
	}
	model, err := GetModel(body.Model)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}
	s.sched.expireRunner(model)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	stopped := s.sched.confirmedStopped(ctx, model)
	if stopped {
		stopped = s.gatewayMetrics.snapshot()["active_slots"].(int) == 0
	}
	c.JSON(http.StatusOK, gin.H{"protocol_version": 1, "model": body.Model, "nonce": body.Nonce, "stopped": stopped, "native_exit_barrier": stopped})
}
