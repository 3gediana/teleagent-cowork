package handler

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type SSEHandler struct{}

func NewSSEHandler() *SSEHandler {
	return &SSEHandler{}
}

func (h *SSEHandler) Events(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	key := c.Query("key")
	if key != "" {
		agent, err := repo.GetAgentByKey(key)
		if err != nil || agent == nil {
			c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Invalid access key"}})
			return
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	client := service.SSEManager.AddClient(projectID)
	defer service.SSEManager.RemoveClient(projectID)

	recent := service.SSEManager.GetRecentBroadcasts(projectID, 5)
	if len(recent) > 0 {
		for _, msg := range recent {
			data, _ := json.Marshal(msg)
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", msg.Header.Type, string(data))
		}
		c.Writer.Flush()
	}

	for {
		select {
		case msg, ok := <-client.Channel:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", msg.Header.Type, string(data))
			c.Writer.Flush()
		case <-client.Quit:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}