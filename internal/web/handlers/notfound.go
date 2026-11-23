package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NotFound renders the 404 page.
func (h *Handler) NotFound(c *gin.Context) {
	h.errorPage(c, http.StatusNotFound, "This page does not exist.")
}
