package bot

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestRegoloLive exercises the real Regolo client against the bot's actual
// ReminderTools schema and the chosen model. It is skipped unless REGOLO_API_KEY
// is set, so normal `go test` runs are unaffected.
//
//	CGO_ENABLED=0 REGOLO_API_KEY=xxx go test ./bot -run TestRegoloLive -v
func TestRegoloLive(t *testing.T) {
	key := os.Getenv("REGOLO_API_KEY")
	if key == "" {
		t.Skip("REGOLO_API_KEY not set; skipping live Regolo test")
	}
	if err := initRegoloClient(key, os.Getenv("REGOLO_MODEL")); err != nil {
		t.Fatalf("initRegoloClient: %v", err)
	}
	ctx := context.Background()

	// 1) General knowledge: tools offered, but a plain question should get a
	//    direct answer (no tool call).
	resp, err := RegoloChat(ctx, []Message{
		{Role: "system", Content: SystemInstruction},
		{Role: "user", Content: "In one short sentence, what is the capital of Norway?"},
	}, append(ReminderTools, SSHTools...))
	if err != nil {
		t.Fatalf("general-knowledge call failed: %v", err)
	}
	m := resp.Choices[0].Message
	if len(m.ToolCalls) != 0 {
		t.Errorf("expected no tool call for a general question, got %d", len(m.ToolCalls))
	}
	if m.Content == "" {
		t.Error("expected a text answer for the general question")
	}
	t.Logf("general-knowledge answer: %q", m.Content)

	// 2) Tool call: a reminder request should trigger add_reminder with the real
	//    (complex) ReminderTools schema, and its arguments must be valid JSON with
	//    the required fields — the exact contract HandleFunctionCallWithContext relies on.
	resp, err = RegoloChat(ctx, []Message{
		{Role: "system", Content: SystemInstruction},
		{Role: "user", Content: "Remind me to call the vet tomorrow at 8pm."},
	}, append(ReminderTools, SSHTools...))
	if err != nil {
		t.Fatalf("reminder call failed: %v", err)
	}
	m = resp.Choices[0].Message
	if len(m.ToolCalls) == 0 {
		t.Fatalf("expected a tool call for a reminder request, got none (content=%q)", m.Content)
	}
	tc := m.ToolCalls[0]
	if tc.Function.Name != "add_reminder" {
		t.Fatalf("expected add_reminder, got %q", tc.Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("tool arguments are not valid JSON (%q): %v", tc.Function.Arguments, err)
	}
	for _, k := range []string{"who", "when", "message"} {
		if _, ok := args[k]; !ok {
			t.Errorf("add_reminder args missing required key %q (got %v)", k, args)
		}
	}
	t.Logf("add_reminder args: %v", args)
}
