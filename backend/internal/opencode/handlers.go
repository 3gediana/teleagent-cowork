package opencode

type ToolCallHandler func(sessionID string, changeID string, projectID string, toolName string, args map[string]interface{})

var toolCallHandler ToolCallHandler

func RegisterToolCallHandler(h ToolCallHandler) {
	toolCallHandler = h
}

func GetToolCallHandler() ToolCallHandler {
	return toolCallHandler
}