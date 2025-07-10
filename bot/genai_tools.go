package bot

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
	"google.golang.org/genai"
)

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

// HandleFunctionCallWithContext processes a function call from the Gemini model with explicit user/channel context.
func HandleFunctionCallWithContext(s *discordgo.Session, i *discordgo.InteractionCreate, call *genai.FunctionCall, userID, channelID string) (*genai.Part, error) {
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
	default:
		return nil, fmt.Errorf("unknown function call: %s", call.Name)
	}
}
