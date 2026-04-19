package agent

import (
	"embed"
	"strings"
)

//go:embed prompts/*.md
var promptFS embed.FS

func getPromptTemplate(name string) (string, error) {
	data, err := promptFS.ReadFile("prompts/" + name)
	if err != nil {
		data, err = promptFS.ReadFile("prompts/" + name)
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(data)), nil
}