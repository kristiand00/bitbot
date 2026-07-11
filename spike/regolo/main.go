// Spike: prove Regolo.ai (OpenAI-compatible) does tool-calling + general
// knowledge with gpt-oss-120b, before migrating bitbot off Google Gemini.
//
// Run:  REGOLO_API_KEY=xxx go run .           (defaults to gpt-oss-120b)
//       REGOLO_MODEL=gemma4-31b go run .       (override the model)
//
// Stdlib only — no external deps, no network fetch needed to build.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const endpoint = "https://api.regolo.ai/v1/chat/completions"

// ---- OpenAI-compatible request/response shapes (minimal subset) ----

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // for role:"tool" replies
	Name       string     `json:"name,omitempty"`         // tool name on the reply
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string
	} `json:"function"`
}

type tool struct {
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
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// One real-ish tool mirroring bitbot's list_reminders (no args).
var reminderTool = tool{
	Type: "function",
	Function: functionSpec{
		Name:        "list_reminders",
		Description: "Lists all reminders for the current user.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	},
}

func main() {
	apiKey := os.Getenv("REGOLO_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: set REGOLO_API_KEY in the environment first, e.g.")
		fmt.Fprintln(os.Stderr, "  ! export REGOLO_API_KEY=your_key_here")
		os.Exit(1)
	}
	model := os.Getenv("REGOLO_MODEL")
	if model == "" {
		model = "gpt-oss-120b"
	}
	client := &http.Client{Timeout: 90 * time.Second}

	fmt.Printf("== Regolo spike, model=%s ==\n\n", model)

	// TEST 1: general knowledge (tools offered, but the model should just answer).
	fmt.Println("--- Test 1: general knowledge (should answer directly) ---")
	resp, err := call(client, apiKey, chatRequest{
		Model: model,
		Tools: []tool{reminderTool},
		Messages: []message{
			{Role: "system", Content: "You are !bit, a helpful assistant. Be brief."},
			{Role: "user", Content: "In one sentence, what is the capital of Norway and one fun fact about it?"},
		},
	})
	if err != nil {
		fmt.Println("FAIL:", err)
	} else {
		m := resp.Choices[0].Message
		fmt.Printf("finish_reason=%s\n", resp.Choices[0].FinishReason)
		fmt.Printf("answer: %s\n\n", m.Content)
	}

	// TEST 2: tool call round-trip. Ask something that needs the tool.
	fmt.Println("--- Test 2: tool-calling round-trip (should call list_reminders) ---")
	history := []message{
		{Role: "system", Content: "You are !bit. Use tools when needed, then summarize the result in natural language."},
		{Role: "user", Content: "What reminders do I have set?"},
	}
	resp, err = call(client, apiKey, chatRequest{Model: model, Tools: []tool{reminderTool}, Messages: history})
	if err != nil {
		fmt.Println("FAIL:", err)
		return
	}
	first := resp.Choices[0].Message
	fmt.Printf("finish_reason=%s, tool_calls=%d\n", resp.Choices[0].FinishReason, len(first.ToolCalls))

	if len(first.ToolCalls) == 0 {
		fmt.Println("NOTE: model answered without calling the tool:")
		fmt.Println(first.Content)
		fmt.Println("\n(If this model never calls tools, try REGOLO_MODEL=Llama-3.3-70B-Instruct or gemma4-31b.)")
		return
	}

	tc := first.ToolCalls[0]
	fmt.Printf("model requested tool: %s(args=%s)\n", tc.Function.Name, tc.Function.Arguments)

	// Append the assistant's tool-call turn, then our fake tool result.
	history = append(history, first)
	history = append(history, message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Name:       tc.Function.Name,
		Content:    `{"status":"success","reminders":[{"id":"r1","when":"tomorrow at 8pm","message":"call the vet"}]}`,
	})

	// Second round: model should turn the tool result into a natural reply.
	resp, err = call(client, apiKey, chatRequest{Model: model, Tools: []tool{reminderTool}, Messages: history})
	if err != nil {
		fmt.Println("FAIL (second round):", err)
		return
	}
	fmt.Printf("final answer: %s\n", resp.Choices[0].Message.Content)

	fmt.Println("\n== Spike done. Tool-calling + general knowledge both work if the two tests above look right. ==")
}

func call(c *http.Client, apiKey string, body chatRequest) (*chatResponse, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	res, err := c.Do(req)
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
