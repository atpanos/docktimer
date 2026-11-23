package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Healthz is a liveness probe that does not touch external systems.
func (h *Handler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": h.deps.Version})
}

// Readyz checks the database and the Docker daemon.
func (h *Handler) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	dbErr := h.deps.Store.Ping(ctx)
	dockerErr := h.deps.Docker.Ping(ctx)

	status := http.StatusOK
	body := gin.H{"status": "ok", "database": "ok", "docker": "ok"}
	if dbErr != nil {
		status = http.StatusServiceUnavailable
		body["status"] = "degraded"
		body["database"] = "error"
	}
	if dockerErr != nil {
		status = http.StatusServiceUnavailable
		body["status"] = "degraded"
		body["docker"] = "error"
	}
	c.JSON(status, body)
}
