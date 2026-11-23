package handlers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/gin-gonic/gin"
)

type logsData struct {
	Logs        []database.ExecutionLog
	Status      string
	Trigger     string
	Container   string
	NextBefore  int64
	HasMore     bool
	Containers  []docker.Container
	DockerErr   string
	TZ          string
	TotalShown  int
	BeforeParam string
	NextURL     string
}

// LogsPage renders the execution log with filters and cursor pagination.
func (h *Handler) LogsPage(c *gin.Context) {
	ctx := c.Request.Context()
	data := logsData{
		Status:    c.Query("status"),
		Trigger:   c.Query("trigger"),
		Container: c.Query("container"),
		TZ:        h.deps.Cfg.DefaultTimezone,
	}
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)

	filter := database.LogFilter{
		BeforeID:    before,
		Status:      database.Status(c.Query("status")),
		Trigger:     database.Trigger(c.Query("trigger")),
		ContainerID: c.Query("container"),
		Limit:       51,
	}
	logs, err := h.deps.Logs.Page(ctx, filter)
	if err != nil {
		h.deps.Log.Error("list logs", "error", err)
	}
	if len(logs) > 50 {
		data.HasMore = true
		logs = logs[:50]
		data.NextBefore = logs[len(logs)-1].ID
	}
	data.Logs = logs
	data.TotalShown = len(logs)
	data.BeforeParam = c.Query("before")
	if data.HasMore {
		q := url.Values{}
		q.Set("before", strconv.FormatInt(data.NextBefore, 10))
		if data.Status != "" {
			q.Set("status", data.Status)
		}
		if data.Trigger != "" {
			q.Set("trigger", data.Trigger)
		}
		if data.Container != "" {
			q.Set("container", data.Container)
		}
		data.NextURL = "/logs?" + q.Encode()
	}

	containers, err := h.deps.Docker.List(ctx)
	if err != nil {
		data.DockerErr = dockerErrorText(err)
	}
	data.Containers = containers

	h.page(c, http.StatusOK, "logs", "Logs", "logs", data)
}
