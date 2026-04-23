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

	// Browser EventSource cannot set an Authorization header, so the
	// dashboard passes the access key as ?key=... and we treat that as
	// the sole auth channel for this endpoint. The key was previously
	// optional, which meant anyone who could guess a project_id could
	// stream its entire event feed (audit reasoning, PR content,
	// agent activity). Now: key is required, must resolve to a real
	// agent, and that agent must have selected this project (or be a
	// human, who can switch projects from the dashboard).
	key := c.Query("key")
	if key == "" {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "UNAUTHORIZED", "message": "key query parameter is required"}})
		return
	}
	agent, err := repo.GetAgentByKey(key)
	if err != nil || agent == nil {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Invalid access key"}})
		return
	}
	if !agent.IsHuman {
		current := ""
		if agent.CurrentProjectID != nil {
			current = *agent.CurrentProjectID
		}
		if current != projectID {
			c.JSON(403, gin.H{"success": false, "error": gin.H{
				"code":    "PROJECT_FORBIDDEN",
				"message": "caller has not selected this project",
			}})
			return
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	client := service.SSEManager.AddClient(projectID)
	defer service.SSEManager.RemoveClient(client.ID)

	// On (re)connect, allow the client to request all messages since a given
	// ID via the standard `Last-Event-ID` header or `?last_event_id=`. This
	// prevents losing events during brief disconnects.
	lastEventID := c.GetHeader("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = c.Query("last_event_id")
	}
	recent := service.SSEManager.GetRecentSince(projectID, lastEventID)
	if len(recent) > 0 {
		for _, msg := range recent {
			data, _ := json.Marshal(msg)
			fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", msg.Header.MessageID, msg.Header.Type, string(data))
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
			fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", msg.Header.MessageID, msg.Header.Type, string(data))
			c.Writer.Flush()
		case <-client.Quit:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}