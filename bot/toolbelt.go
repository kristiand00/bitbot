package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
)

// registeredTool is a tool reached indirectly through the toolbelt
// (find_tools/call_tool) rather than being registered as a top-level function
// tool. Keeping these out of the model's per-request tool list keeps token usage
// flat no matter how many tools (local or remote/MCP) are available.
type registeredTool struct {
	Name        string
	Description string
	InputSchema any    // JSON Schema for the tool's arguments
	Source      string // "" for local tools, else the MCP server name
	AdminOnly   bool
	Destructive bool // requires an admin Confirm/Cancel button before running
	Invoke      func(ctx context.Context, userID, channelID, guildID string, args map[string]any) (string, error)
}

var (
	toolRegistry   = map[string]*registeredTool{}
	toolRegistryMu sync.RWMutex
)

func registerTool(t *registeredTool) {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	toolRegistry[t.Name] = t
	log.Infof("registered toolbelt tool: %s (adminOnly=%v destructive=%v)", t.Name, t.AdminOnly, t.Destructive)
}

func lookupTool(name string) *registeredTool {
	toolRegistryMu.RLock()
	defer toolRegistryMu.RUnlock()
	return toolRegistry[name]
}

// unregisterSource removes every tool registered by the given source (MCP server
// name) and returns how many were removed.
func unregisterSource(source string) int {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	n := 0
	for name, t := range toolRegistry {
		if t.Source == source {
			delete(toolRegistry, name)
			n++
		}
	}
	return n
}

func listRegisteredTools() []*registeredTool {
	toolRegistryMu.RLock()
	defer toolRegistryMu.RUnlock()
	out := make([]*registeredTool, 0, len(toolRegistry))
	for _, t := range toolRegistry {
		out = append(out, t)
	}
	return out
}

// ToolbeltTools are the only extended-tool entries the model sees directly;
// everything else is discovered and invoked through them.
var ToolbeltTools = []Tool{
	{
		Type: "function",
		Function: functionSpec{
			Name:        "find_tools",
			Description: "Discover extended tools available through the toolbelt (SSH management, backups, and other integrations). Returns each matching tool's name, description, and JSON input schema. Call this before call_tool to learn the exact tool name and its arguments.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Optional case-insensitive filter matched against tool names and descriptions. Omit or leave empty to list every tool.",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "call_tool",
			Description: "Invoke a toolbelt tool discovered via find_tools. Provide the exact tool name and an arguments object matching that tool's input schema. Some tools are admin-only; destructive tools require the user to confirm with a button before they run.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "The exact tool name, as returned by find_tools.",
					},
					"arguments": map[string]interface{}{
						"type":        "object",
						"description": "The arguments object for the tool, matching its input schema. Use an empty object if the tool takes no arguments.",
					},
				},
				"required": []string{"name"},
			},
		},
	},
}

// handleFindTools returns a JSON catalog of registered tools, optionally filtered
// by a query, including each tool's input schema so the model can build a valid
// call_tool request in one step.
func handleFindTools(args map[string]any) string {
	query := strings.ToLower(strings.TrimSpace(getStr(args, "query")))

	type toolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"input_schema"`
		AdminOnly   bool   `json:"admin_only"`
		Destructive bool   `json:"destructive"`
	}
	infos := []toolInfo{}
	for _, t := range listRegisteredTools() {
		if query != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.Description), query) {
			continue
		}
		infos = append(infos, toolInfo{t.Name, t.Description, t.InputSchema, t.AdminOnly, t.Destructive})
	}
	b, err := json.Marshal(map[string]any{"tools": infos, "count": len(infos)})
	if err != nil {
		return jsonResult("error", "failed to serialize tool list")
	}
	return string(b)
}

// handleCallTool dispatches a call to a registered tool, enforcing the admin and
// confirmation policy. Destructive tools are not run here: a Confirm/Cancel
// prompt is sent and execution happens on confirmation (see handleToolbeltButton).
func handleCallTool(s *discordgo.Session, userID, channelID, guildID string, args map[string]any) string {
	name := getStr(args, "name")
	if name == "" {
		return jsonResult("error", "call_tool requires a 'name'")
	}
	toolArgs, _ := args["arguments"].(map[string]any)
	if toolArgs == nil {
		toolArgs = map[string]any{}
	}

	t := lookupTool(name)
	if t == nil {
		return jsonResult("error", fmt.Sprintf("unknown tool %q; use find_tools to list available tools", name))
	}

	if t.AdminOnly && !authorizeSSH(s, guildID, userID) {
		return jsonResult("error", "You are not authorized to use this tool (admin only).")
	}

	if t.Destructive {
		id := newPendingID()
		storePending(id, &pendingAction{tool: t, args: toolArgs, userID: userID, channelID: channelID, guildID: guildID})
		if err := sendConfirmPrompt(s, channelID, t, toolArgs, id); err != nil {
			deletePending(id)
			log.Errorf("failed to send confirmation prompt for %s: %v", name, err)
			return jsonResult("error", "failed to send the confirmation prompt")
		}
		return jsonResult("pending", fmt.Sprintf("%q is a destructive action. A Confirm/Cancel prompt was sent and an admin must approve it. The tool has NOT run yet — do not retry; wait for the user to confirm.", name))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := t.Invoke(ctx, userID, channelID, guildID, toolArgs)
	if err != nil {
		return jsonResult("error", err.Error())
	}
	return result
}

// --- Confirmation flow for destructive tools ---

type pendingAction struct {
	tool      *registeredTool
	args      map[string]any
	userID    string
	channelID string
	guildID   string
}

var (
	pendingActions   = map[string]*pendingAction{}
	pendingActionsMu sync.Mutex
	pendingCounter   uint64
)

func newPendingID() string { return fmt.Sprintf("%d", atomic.AddUint64(&pendingCounter, 1)) }

func storePending(id string, a *pendingAction) {
	pendingActionsMu.Lock()
	defer pendingActionsMu.Unlock()
	pendingActions[id] = a
}

func takePending(id string) *pendingAction {
	pendingActionsMu.Lock()
	defer pendingActionsMu.Unlock()
	a := pendingActions[id]
	delete(pendingActions, id)
	return a
}

func deletePending(id string) {
	pendingActionsMu.Lock()
	defer pendingActionsMu.Unlock()
	delete(pendingActions, id)
}

func sendConfirmPrompt(s *discordgo.Session, channelID string, t *registeredTool, args map[string]any, id string) error {
	argsJSON, _ := json.Marshal(args)
	content := fmt.Sprintf("⚠️ **Destructive action requested:** `%s`\n```json\n%s\n```\nAn admin must confirm.", t.Name, string(argsJSON))
	content = truncateToLimit(content, discordMessageLimit)
	_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: content,
		Components: []discordgo.MessageComponent{
			&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				&discordgo.Button{Label: "Confirm", Style: discordgo.DangerButton, CustomID: "tb_confirm_" + id},
				&discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: "tb_cancel_" + id},
			}},
		},
	})
	return err
}

// handleToolbeltButton handles the Confirm/Cancel buttons for a pending
// destructive tool. Returns true if it handled the interaction. Only admins may
// confirm or cancel.
func handleToolbeltButton(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	customID := i.MessageComponentData().CustomID
	var id string
	var confirm bool
	switch {
	case strings.HasPrefix(customID, "tb_confirm_"):
		id, confirm = strings.TrimPrefix(customID, "tb_confirm_"), true
	case strings.HasPrefix(customID, "tb_cancel_"):
		id = strings.TrimPrefix(customID, "tb_cancel_")
	default:
		return false
	}

	if !authorizeSSH(s, i.GuildID, getUserID(i)) {
		respondWithMessage(s, i, "Only an admin can confirm or cancel this action.")
		return true
	}

	a := takePending(id)
	if a == nil {
		respondWithMessage(s, i, "This confirmation has expired or was already handled.")
		return true
	}

	if !confirm {
		respondWithMessage(s, i, fmt.Sprintf("❌ Cancelled `%s`.", a.tool.Name))
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := a.tool.Invoke(ctx, a.userID, a.channelID, a.guildID, a.args)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("⚠️ `%s` failed: %v", a.tool.Name, err))
		return true
	}
	respondWithMessage(s, i, truncateToLimit(fmt.Sprintf("✅ Executed `%s`:\n%s", a.tool.Name, result), discordMessageLimit))
	return true
}

// --- Local (SSH) tool registration ---

func getStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func getBool(m map[string]any, k string) bool  { b, _ := m[k].(bool); return b }

func sshResult(resp string, err error) (string, error) {
	if err != nil {
		return jsonResult("error", resp), nil
	}
	return jsonResult("success", resp), nil
}

// registerSSHTools moves the SSH tools behind the toolbelt. They are admin-only
// (as before) but not flagged destructive, preserving their existing UX.
func registerSSHTools() {
	invokers := map[string]func(userID, guildID string, a map[string]any) (string, error){
		"generate_ssh_key":     func(u, g string, a map[string]any) (string, error) { return sshResult(GenerateSSHKeyCore(getBool(a, "regenerate"))) },
		"show_ssh_public_key":  func(u, g string, a map[string]any) (string, error) { return sshResult(ShowSSHPublicKeyCore()) },
		"connect_ssh_server":   func(u, g string, a map[string]any) (string, error) { return sshResult(ConnectSSHServerCore(u, g, getStr(a, "connection_details"))) },
		"execute_ssh_command":  func(u, g string, a map[string]any) (string, error) { return sshResult(ExecuteSSHCommandCore(u, g, getStr(a, "command"))) },
		"close_ssh_connection": func(u, g string, a map[string]any) (string, error) { return sshResult(CloseSSHConnectionCore(u, g)) },
		"list_ssh_servers":     func(u, g string, a map[string]any) (string, error) { return sshResult(ListSSHServersCore(u, g)) },
	}
	for _, def := range SSHTools {
		inv := invokers[def.Function.Name]
		if inv == nil {
			continue
		}
		fn := inv
		registerTool(&registeredTool{
			Name:        def.Function.Name,
			Description: def.Function.Description,
			InputSchema: def.Function.Parameters,
			AdminOnly:   true,
			Invoke: func(ctx context.Context, userID, channelID, guildID string, args map[string]any) (string, error) {
				return fn(userID, guildID, args)
			},
		})
	}
}
