package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(serveURL string) *Client {
	return &Client{
		baseURL: serveURL,
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
}

type MessagePart struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCall struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	ToolName string                 `json:"toolName"`
	Args     map[string]interface{} `json:"args"`
}

type Message struct {
	ID    string         `json:"id"`
	Role  string         `json:"role"`
	Parts []MessagePart  `json:"parts"`
}

type SendMessageResponse struct {
	Info   Message        `json:"info"`
	Parts  []MessagePart  `json:"parts"`
}

func (c *Client) CreateSession(title string) (*Session, error) {
	body := map[string]interface{}{}
	if title != "" {
		body["title"] = title
	}
	jsonBody, _ := json.Marshal(body)

	resp, err := c.httpClient.Post(c.baseURL+"/session", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create session failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("failed to decode session response: %w", err)
	}
	return &session, nil
}

type SendMessageRequest struct {
	Content string `json:"-"`
	Agent   string `json:"agent,omitempty"`
	Model   string `json:"model,omitempty"`
}

func (c *Client) SendMessage(sessionID string, message string, agent string, modelProvider string, modelID string) (*SendMessageResponse, error) {
	body := map[string]interface{}{
		"parts": []map[string]interface{}{
			{
				"type": "text",
				"text": message,
			},
		},
	}
	if agent != "" {
		body["agent"] = agent
	}
	if modelProvider != "" && modelID != "" {
		body["model"] = map[string]string{
			"providerID": modelProvider,
			"modelID":    modelID,
		}
	}
	jsonBody, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/session/%s/message", c.baseURL, sessionID)
	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("send message failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result SendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode message response: %w", err)
	}
	return &result, nil
}

func (c *Client) GetMessages(sessionID string) ([]Message, error) {
	url := fmt.Sprintf("%s/session/%s/message", c.baseURL, sessionID)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get messages failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var messages []Message
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return nil, fmt.Errorf("failed to decode messages: %w", err)
	}
	return messages, nil
}

func (c *Client) DeleteSession(sessionID string) error {
	url := fmt.Sprintf("%s/session/%s", c.baseURL, sessionID)
	req, _ := http.NewRequest("DELETE", url, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("delete session failed (%d)", resp.StatusCode)
	}
	return nil
}

func (c *Client) Health() bool {
	resp, err := c.httpClient.Get(c.baseURL + "/global/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}