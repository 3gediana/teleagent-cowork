package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
)

type SSEMessage struct {
	Header  SSEHeader `json:"header"`
	Payload gin.H      `json:"payload"`
	Meta    gin.H      `json:"meta"`
}

type SSEHeader struct {
	MessageID string `json:"messageId"`
	Type      string `json:"version"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

type SSEClient struct {
	ID       string
	Channel  chan SSEMessage
	Quit     chan struct{}
}

type SSEManagerStruct struct {
	clients   map[string]*SSEClient
	mu        sync.RWMutex
}

var SSEManager = &SSEManagerStruct{
	clients: make(map[string]*SSEClient),
}

func (m *SSEManagerStruct) AddClient(clientID string) *SSEClient {
	client := &SSEClient{
		ID:      clientID,
		Channel: make(chan SSEMessage, 10),
		Quit:    make(chan struct{}),
	}
	m.mu.Lock()
	m.clients[clientID] = client
	m.mu.Unlock()
	return client
}

func (m *SSEManagerStruct) RemoveClient(clientID string) {
	m.mu.Lock()
	if client, ok := m.clients[clientID]; ok {
		close(client.Quit)
		delete(m.clients, clientID)
	}
	m.mu.Unlock()
}

func (m *SSEManagerStruct) BroadcastToProject(projectID string, eventType string, payload gin.H) {
	msg := SSEMessage{
		Header: SSEHeader{
			MessageID: model.GenerateID("msg"),
			Type:      eventType,
			Version:   "1.0",
			Timestamp: time.Now().Format(time.RFC3339),
		},
		Payload: payload,
		Meta: gin.H{
			"project_id": projectID,
		},
	}

	jsonData, _ := json.Marshal(msg)
	msgKey := "a3c:broadcast:" + projectID

	ctx := context.Background()
	model.RDB.LPush(ctx, msgKey, string(jsonData))
	model.RDB.LTrim(ctx, msgKey, 0, 99)
	model.RDB.Expire(ctx, msgKey, 24*time.Hour)

	ackKey := fmt.Sprintf("a3c:broadcast:%s:%s:acked", projectID, msg.Header.MessageID)
	model.RDB.Expire(ctx, ackKey, 24*time.Hour)

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, client := range m.clients {
		if client.ID == projectID {
			select {
			case client.Channel <- msg:
			default:
			}
		}
	}
}

func (m *SSEManagerStruct) GetRecentBroadcasts(projectID string, limit int64) []SSEMessage {
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, limit-1).Result()
	if err != nil {
		return nil
	}
	messages := make([]SSEMessage, 0, len(data))
	for _, d := range data {
		var msg SSEMessage
		if json.Unmarshal([]byte(d), &msg) == nil {
			messages = append(messages, msg)
		}
	}
	return messages
}

func (m *SSEManagerStruct) GetUnackedBroadcasts(projectID string, agentID string) []SSEMessage {
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, 49).Result()
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var result []SSEMessage
	for _, d := range data {
		var msg SSEMessage
		if json.Unmarshal([]byte(d), &msg) != nil {
			continue
		}
		if seen[msg.Header.Type] {
			continue
		}
		seen[msg.Header.Type] = true

		ackKey := fmt.Sprintf("a3c:broadcast:%s:%s:acked", projectID, msg.Header.MessageID)
		acked, _ := model.RDB.SIsMember(ctx, ackKey, agentID).Result()
		if acked {
			continue
		}

		result = append(result, msg)
		model.RDB.SAdd(ctx, ackKey, agentID)
	}
	return result
}

func BroadcastEvent(projectID string, eventType string, payload gin.H) {
	SSEManager.BroadcastToProject(projectID, eventType, payload)
}