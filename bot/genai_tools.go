package bot

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
	"google.golang.org/genai"
)

// SSHTools defines the tools available to the Gemini model for SSH capabilities.
var SSHTools = []*genai.Tool{
	{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "generate_ssh_key",
				Description: "Generates and saves a new SSH key pair. Fails if the user is not an admin.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"regenerate": {
							Type:        genai.TypeBoolean,
							Description: "If true, overwrites any existing keys. If false, only generates if keys do not already exist.",
						},
					},
					Required: []string{"regenerate"},
				},
			},
			{
				Name:        "show_ssh_public_key",
				Description: "Shows the bot's public SSH key so it can be added to a remote server. Fails if the user is not an admin.",
				Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}},
			},
			{
				Name:        "connect_ssh_server",
				Description: "Connects to a remote server via SSH. Fails if the user is not an admin.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"connection_details": {
							Type:        genai.TypeString,
							Description: "Connection details in the format username@remote-host:port",
						},
					},
					Required: []string{"connection_details"},
				},
			},
			{
				Name:        "execute_ssh_command",
				Description: "Executes a command on the currently connected SSH server. Fails if the user is not an admin.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"command": {
							Type:        genai.TypeString,
							Description: "The command to execute on the remote server (e.g., 'ls -la', 'uptime').",
						},
					},
					Required: []string{"command"},
				},
			},
			{
				Name:        "close_ssh_connection",
				Description: "Closes the current SSH connection. Fails if the user is not an admin.",
				Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}},
			},
			{
				Name:        "list_ssh_servers",
				Description: "Lists saved SSH servers for this guild. Fails if the user is not an admin.",
				Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}},
			},
		},
	},
}

// ReminderTools defines the tools available to the Gemini model for reminders.
var ReminderTools = []*genai.Tool{
	{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "add_reminder",
				Description: "Adds a reminder for a user.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"who": {
							Type:        genai.TypeString,
							Description: "The user to remind. Can be a user ID or '@me'.",
						},
						"when": {
							Type: genai.TypeString,
							Description: `When to send the reminder. 
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
						"message": {
							Type:        genai.TypeString,
							Description: "The reminder message.",
						},
					},
					Required: []string{"who", "when", "message"},
				},
			},
			{
				Name:        "list_reminders",
				Description: "Lists all reminders for the user.",
				Parameters:  &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}},
			},
			{
				Name:        "delete_reminder",
				Description: "Deletes a reminder by its ID.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"id": {
							Type:        genai.TypeString,
							Description: "The ID of the reminder to delete.",
						},
					},
					Required: []string{"id"},
				},
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

// HandleFunctionCallWithContext processes a function call from the Gemini model with explicit user/channel context.
func HandleFunctionCallWithContext(s *discordgo.Session, i *discordgo.InteractionCreate, call *genai.FunctionCall, userID, channelID, guildID string) (*genai.Part, error) {
	switch call.Name {
	case "add_reminder":
		who, _ := call.Args["who"].(string)
		when, _ := call.Args["when"].(string)
		message, _ := call.Args["message"].(string)
		resp, err := AddReminderCore(userID, channelID, who, when, message)
		if err != nil {
			// Error: return only Text
			return &genai.Part{
				Text: resp,
			}, nil
		}
		// Success: return only FunctionResponse
		return &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				Name: "add_reminder",
				Response: map[string]interface{}{
					"status":  "success",
					"message": resp,
				},
			},
		}, nil
	case "list_reminders":
		resp, err := ListRemindersCore(userID)
		if err != nil {
			return &genai.Part{
				Text: resp,
			}, nil
		}
		return &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				Name: "list_reminders",
				Response: map[string]interface{}{
					"status":  "success",
					"message": resp,
				},
			},
		}, nil
	case "delete_reminder":
		id, _ := call.Args["id"].(string)
		resp, err := DeleteReminderCore(userID, id)
		if err != nil {
			return &genai.Part{
				Text: resp,
			}, nil
		}
		return &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				Name: "delete_reminder",
				Response: map[string]interface{}{
					"status":  "success",
					"message": resp,
				},
			},
		}, nil
	case "generate_ssh_key":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		regenerate, _ := call.Args["regenerate"].(bool)
		resp, err := GenerateSSHKeyCore(regenerate)
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	case "show_ssh_public_key":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		resp, err := ShowSSHPublicKeyCore()
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	case "connect_ssh_server":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		connectionDetails, _ := call.Args["connection_details"].(string)
		resp, err := ConnectSSHServerCore(userID, guildID, connectionDetails)
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	case "execute_ssh_command":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		command, _ := call.Args["command"].(string)
		resp, err := ExecuteSSHCommandCore(userID, guildID, command)
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	case "close_ssh_connection":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		resp, err := CloseSSHConnectionCore(userID, guildID)
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	case "list_ssh_servers":
		if !authorizeSSH(s, guildID, userID) {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": "You are not authorized to use SSH commands."}}}, nil
		}
		resp, err := ListSSHServersCore(userID, guildID)
		if err != nil {
			return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "error", "message": resp}}}, nil
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: call.Name, Response: map[string]interface{}{"status": "success", "message": resp}}}, nil

	default:
		return nil, fmt.Errorf("unknown function call: %s", call.Name)
	}
}
