package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// regoloEndpoint is the OpenAI-compatible chat completions endpoint for Regolo.ai.
const regoloEndpoint = "https://api.regolo.ai/v1/chat/completions"

// ---- OpenAI-compatible request/response shapes (minimal subset) ----

// Message is a single chat message in the OpenAI-compatible format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // for role:"tool" replies
	Name       string     `json:"name,omitempty"`         // tool name on the reply
}

// ToolCall represents a tool/function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string
	} `json:"function"`
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Package-level configuration for the Regolo client.
var (
	regoloAPIKey string
	regoloModel  string
)

// initRegoloClient stores the API key and model for later chat requests.
// If model is empty it defaults to "gpt-oss-120b". Returns an error if apiKey is empty.
func initRegoloClient(apiKey, model string) error {
	if apiKey == "" {
		return fmt.Errorf("regolo API key is not provided")
	}
	if model == "" {
		model = "gpt-oss-120b"
	}
	regoloAPIKey = apiKey
	regoloModel = model
	return nil
}

// RegoloChat POSTs a chat completion request with the given messages and tools,
// returning the parsed response.
func RegoloChat(ctx context.Context, messages []Message, tools []Tool) (*chatResponse, error) {
	if regoloAPIKey == "" {
		return nil, fmt.Errorf("regolo client is not initialized")
	}

	body := chatRequest{
		Model:    regoloModel,
		Messages: messages,
		Tools:    tools,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", regoloEndpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+regoloAPIKey)

	client := &http.Client{Timeout: 90 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, string(raw))
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w (raw: %s)", err, string(raw))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("api error: %s (%s)", parsed.Error.Message, parsed.Error.Type)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response: %s", string(raw))
	}
	return &parsed, nil
}
