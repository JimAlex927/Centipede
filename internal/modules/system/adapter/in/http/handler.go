package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type DatabasePinger interface {
	Ping(context.Context) error
}

func Register(router gin.IRouter, database DatabasePinger, environment string) {
	live := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "environment": environment})
	}
	readiness := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"code": "not_ready", "message": "database is unavailable",
			}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}

	router.GET("/health/live", live)
	router.GET("/health/ready", readiness)
	// Keep the upstream Docmost health paths available for existing probes.
	router.GET("/api/health", readiness)
	router.GET("/api/health/live", live)
	router.GET("/api/health/ready", readiness)
}
