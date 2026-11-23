package handlers

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/atpanos/docktimer/internal/database"
	"github.com/atpanos/docktimer/internal/docker"
	"github.com/atpanos/docktimer/internal/web/middleware"
	"github.com/gin-gonic/gin"
)

type nextRun struct {
	Rule database.Rule
	At   time.Time
	OK   bool
}

type dashboardData struct {
	Running     int
	Stopped     int
	Total       int
	RulesActive int
	RulesTotal  int
	DockerErr   string
	Next        []nextRun
	Recent      []database.ExecutionLog
	Failures    []database.ExecutionLog
}

// Dashboard renders the overview page.
func (h *Handler) Dashboard(c *gin.Context) {
	ctx := c.Request.Context()
	data := dashboardData{}

	containers, err := h.deps.Docker.List(ctx)
	if err != nil {
		data.DockerErr = dockerErrorText(err)
	}
	data.Total = len(containers)
	for _, ct := range containers {
		if ct.Running() {
			data.Running++
		} else {
			data.Stopped++
		}
	}

	rulesList, err := h.deps.Rules.List(ctx)
	if err != nil {
		h.deps.Log.Error("dashboard: list rules", "error", err)
	}
	data.RulesTotal = len(rulesList)
	for _, r := range rulesList {
		if !r.Enabled {
			continue
		}
		data.RulesActive++
		at, ok := h.deps.Scheduler.Next(r.ID)
		data.Next = append(data.Next, nextRun{Rule: r, At: at, OK: ok})
	}
	sort.Slice(data.Next, func(i, j int) bool {
		if data.Next[i].OK != data.Next[j].OK {
			return data.Next[i].OK
		}
		return data.Next[i].At.Before(data.Next[j].At)
	})
	if len(data.Next) > 8 {
		data.Next = data.Next[:8]
	}

	data.Recent, _ = h.deps.Logs.Recent(ctx, 10)
	data.Failures, _ = h.deps.Logs.Failures(ctx, 5)

	h.page(c, http.StatusOK, "dashboard", "Dashboard", "dashboard", data)
}

type containersData struct {
	Containers []docker.Container
	DockerErr  string
	IsAdmin    bool
}

// ContainersPage renders the container overview.
func (h *Handler) ContainersPage(c *gin.Context) {
	h.page(c, http.StatusOK, "containers", "Containers", "containers", h.containersData(c))
}

// ContainersTable renders the HTMX-refreshable container table.
func (h *Handler) ContainersTable(c *gin.Context) {
	h.fragment(c, http.StatusOK, "container_table", h.containersData(c))
}

func (h *Handler) containersData(c *gin.Context) containersData {
	containers, err := h.deps.Docker.List(c.Request.Context())
	user, _ := middleware.CurrentUser(c)
	data := containersData{Containers: containers, IsAdmin: user != nil && user.IsAdmin()}
	if err != nil {
		data.DockerErr = dockerErrorText(err)
	}
	return data
}

// ContainerAction executes a manual START/STOP/RESTART action.
func (h *Handler) ContainerAction(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	action := database.Action(strings.ToUpper(c.Param("action")))
	if !action.Valid() {
		h.errorPage(c, http.StatusBadRequest, "Unbekannte Aktion.")
		return
	}
	id := c.Param("id")
	ctx := c.Request.Context()

	name := id
	if ct, err := h.deps.Docker.Find(ctx, id); err == nil {
		name = ct.Name
	}

	start := time.Now()
	err := h.deps.Docker.Execute(ctx, action, id)
	elapsed := time.Since(start).Milliseconds()

	entry := database.ExecutionLog{
		ContainerID:   id,
		ContainerName: name,
		Action:        action,
		Trigger:       database.TriggerManual,
		Status:        database.StatusSuccess,
		DurationMS:    elapsed,
	}
	if err != nil {
		entry.Status = database.StatusFailed
		entry.Message = err.Error()
	}
	if _, recErr := h.deps.Logs.Record(ctx, entry); recErr != nil {
		h.deps.Log.Error("record manual action", "error", recErr)
	}

	if isHTMX(c) {
		if err != nil {
			h.toast(c, "error", "Aktion fehlgeschlagen: "+dockerErrorText(err))
		} else {
			h.toast(c, "success", "Container "+name+" was "+actionText(action)+".")
		}
		h.fragment(c, http.StatusOK, "container_table", h.containersData(c))
		return
	}

	if err != nil {
		h.page(c, http.StatusOK, "containers", "Containers", "containers", containersData{
			Containers: h.containersData(c).Containers,
			DockerErr:  dockerErrorText(err),
		})
		return
	}
	redirectWithFlash(c, "/containers", "action_ok")
}
