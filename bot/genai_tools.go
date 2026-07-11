package bot

import (
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// SSHTools defines the tools available to the model for SSH capabilities.
var SSHTools = []Tool{
	{
		Type: "function",
		Function: functionSpec{
			Name:        "generate_ssh_key",
			Description: "Generates and saves a new SSH key pair. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"regenerate": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, overwrites any existing keys. If false, only generates if keys do not already exist.",
					},
				},
				"required": []string{"regenerate"},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "show_ssh_public_key",
			Description: "Shows the bot's public SSH key so it can be added to a remote server. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "connect_ssh_server",
			Description: "Connects to a remote server via SSH. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"connection_details": map[string]interface{}{
						"type":        "string",
						"description": "Connection details in the format username@remote-host:port",
					},
				},
				"required": []string{"connection_details"},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "execute_ssh_command",
			Description: "Executes a command on the currently connected SSH server. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The command to execute on the remote server (e.g., 'ls -la', 'uptime').",
					},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "close_ssh_connection",
			Description: "Closes the current SSH connection. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "list_ssh_servers",
			Description: "Lists saved SSH servers for this guild. Fails if the user is not an admin.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	},
}

// ReminderTools defines the tools available to the model for reminders.
var ReminderTools = []Tool{
	{
		Type: "function",
		Function: functionSpec{
			Name:        "add_reminder",
			Description: "Adds a reminder for a user.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"who": map[string]interface{}{
						"type":        "string",
						"description": "The user to remind. Can be a user ID or '@me'.",
					},
					"when": map[string]interface{}{
						"type": "string",
						"description": `When to send the reminder.
Accepted formats:
- "in 10m", "in 2h", "in 3d" (duration)
- "every 10m", "every 2h", "every 3d" (recurring duration)
- "tomorrow at 8pm", "next monday at 9:30am", "today at 8pm", "at 8pm", "8pm", "20:00" (specific time)
- "every day at 8am", "every monday 8pm" (recurring time)
Always convert user input to one of these formats before calling this tool.
Do NOT remove spaces between words in time expressions. Always use the exact format, e.g., 'tomorrow at 8pm', not 'tomorrowat8pm'.
If the time has already passed today, set the reminder for tomorrow.
If a specific time is not supported, offer to set a reminder for the equivalent duration (e.g., "in 24 hours") instead.`,
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The reminder message.",
					},
				},
				"required": []string{"who", "when", "message"},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "list_reminders",
			Description: "Lists all reminders for the user.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	},
	{
		Type: "function",
		Function: functionSpec{
			Name:        "delete_reminder",
			Description: "Deletes a reminder by its ID.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "The ID of the reminder to delete.",
					},
				},
				"required": []string{"id"},
			},
		},
	},
}

func authorizeSSH(s *discordgo.Session, guildID, userID string) bool {
	// Attempt to get the member to check roles if we have a session and guildID
	if s != nil && guildID != "" {
		member, err := s.GuildMember(guildID, userID)
		if err == nil {
			return CheckAdmin(userID, member.Roles)
		}
	}
	// Fallback to checking just the userID
	return CheckAdmin(userID, nil)
}

// jsonResult marshals a status/message object into a JSON string for a tool reply.
func jsonResult(status, message string) string {
	b, err := json.Marshal(map[string]interface{}{
		"status":  status,
		"message": message,
	})
	if err != nil {
		return fmt.Sprintf(`{"status":%q,"message":%q}`, status, message)
	}
	return string(b)
}

// HandleFunctionCallWithContext processes a tool call from the model with explicit
// user/channel context. It returns a JSON-encoded result string.
func HandleFunctionCallWithContext(s *discordgo.Session, i *discordgo.InteractionCreate, call *ToolCall, userID, channelID, guildID string) (string, error) {
	name := call.Function.Name

	// Parse the JSON arguments string into a map to extract args.
	args := map[string]any{}
	if call.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("failed to parse arguments for %s: %w", name, err)
		}
	}

	switch name {
	case "add_reminder":
		who, _ := args["who"].(string)
		when, _ := args["when"].(string)
		message, _ := args["message"].(string)
		resp, err := AddReminderCore(userID, channelID, who, when, message)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil
	case "list_reminders":
		resp, err := ListRemindersCore(userID)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil
	case "delete_reminder":
		id, _ := args["id"].(string)
		resp, err := DeleteReminderCore(userID, id)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil
	case "generate_ssh_key":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		regenerate, _ := args["regenerate"].(bool)
		resp, err := GenerateSSHKeyCore(regenerate)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	case "show_ssh_public_key":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		resp, err := ShowSSHPublicKeyCore()
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	case "connect_ssh_server":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		connectionDetails, _ := args["connection_details"].(string)
		resp, err := ConnectSSHServerCore(userID, guildID, connectionDetails)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	case "execute_ssh_command":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		command, _ := args["command"].(string)
		resp, err := ExecuteSSHCommandCore(userID, guildID, command)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	case "close_ssh_connection":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		resp, err := CloseSSHConnectionCore(userID, guildID)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	case "list_ssh_servers":
		if !authorizeSSH(s, guildID, userID) {
			return jsonResult("error", "You are not authorized to use SSH commands."), nil
		}
		resp, err := ListSSHServersCore(userID, guildID)
		if err != nil {
			return jsonResult("error", resp), nil
		}
		return jsonResult("success", resp), nil

	default:
		return "", fmt.Errorf("unknown function call: %s", name)
	}
}
