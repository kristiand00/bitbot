package bot

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"google.golang.org/genai"
)

// Model name constants
const (
	AudioModelName    = "gemini-2.5-flash-preview-05-20"
	TextModelName     = "gemini-2.5-flash-preview-05-20"
	SystemInstruction = `
your name is !bit you are a discord bot, you use brief answers until asked to elaborate or explain. 
You can also set reminders for users.

When a user asks for a reminder, always convert their time expression to one of the following accepted formats before calling the reminder tool:
- "in 10m", "in 2h", "in 3d" (duration)
- "tomorrow 8pm", "tomorrow at 8pm" (specific time)
- "next monday 9:30am", "next monday at 9:30am" (specific time)
- "every day at 8am", "every monday 8pm" (recurring)
- "today 8pm", "today at 8pm"
- "at 8pm", "8pm", "20:00"

If a user requests a reminder for a specific date/time and it is not supported, offer to set a reminder for the equivalent duration instead (e.g., "Would you like me to set a reminder for 'in 24 hours' instead?").

If a tool returns an error message (as plain text), immediately reply to the user with that error and do not call the tool again unless the user asks for another attempt.

After calling a tool, always reply to the user in natural language summarizing the result. Do not call another tool unless the user explicitly asks for another action.

If the time has already passed today, set the reminder for tomorrow.`
)

var (
	geminiClient *genai.Client
	chatSessions = make(map[string]*genai.Chat)
	httpClient   = &http.Client{
		Timeout: 30 * time.Second,
	}
	geminiAPIKey string // Store the API key for REST API calls

	// Rate limiting
	requestCount         int
	lastRequestTime      time.Time
	requestMutex         sync.Mutex
	rateLimitWindow      = 60 * time.Second // 1 minute window
	maxRequestsPerMinute = 50               // Conservative limit to stay under the 60/minute free tier limit
)

// AudioRequest represents the request body for the Gemini API
type AudioRequest struct {
	Contents []struct {
		Parts []struct {
			Text       string `json:"text,omitempty"`
			InlineData *struct {
				MimeType string `json:"mimeType"`
				Data     string `json:"data"`
			} `json:"inlineData,omitempty"`
		} `json:"parts"`
	} `json:"contents"`
}

// LiveConfig represents the configuration for the Live API
type LiveConfig struct {
	ModelName         string `json:"modelName"`
	ProactivityConfig struct {
		ProactiveAudio bool `json:"proactiveAudio"`
	} `json:"proactivityConfig"`
	AffectiveDialogConfig struct {
		EnableAffectiveDialog bool `json:"enableAffectiveDialog"`
	} `json:"affectiveDialogConfig"`
}

func InitGeminiClient(apiKey string) error {
	startTime := time.Now()
	log.Infof("Starting Gemini client initialization at %v", startTime)

	if apiKey == "" {
		return fmt.Errorf("Gemini API key is not provided")
	}
	geminiAPIKey = apiKey // Store the API key
	log.Info("API key stored, creating client context...")

	ctx := context.Background()
	log.Info("Creating new Gemini client...")
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		log.Errorf("Failed to create Generative Client: %v", err)
		return fmt.Errorf("failed to create Generative Client: %w", err)
	}
	log.Infof("Gemini client created successfully after %v", time.Since(startTime))

	geminiClient = client
	lastRequestTime = time.Now()

	// Test the text model availability only
	log.Info("Starting model availability tests...")
	if err := testTextModelAvailability(ctx); err != nil {
		log.Warnf("Failed to test text model availability: %v", err)
	}

	log.Infof("GenAI Client initialization completed in %v", time.Since(startTime))
	return nil
}

// Only test text model availability
func testTextModelAvailability(ctx context.Context) error {
	startTime := time.Now()

	log.Info("Testing text model availability...")

	// Test text model with a simple prompt
	contents := []*genai.Content{
		{
			Parts: []*genai.Part{
				genai.NewPartFromText("test"),
			},
		},
	}

	_, err := geminiClient.Models.GenerateContent(ctx, TextModelName, contents, nil)
	if err != nil {
		return fmt.Errorf("text model %s is not available: %v", TextModelName, err)
	}
	log.Infof("Text model %s is available (test took %v)", TextModelName, time.Since(startTime))
	return nil
}

// checkRateLimit checks if we're within rate limits and returns true if we can proceed
func checkRateLimit() bool {
	requestMutex.Lock()
	defer requestMutex.Unlock()

	now := time.Now()
	if now.Sub(lastRequestTime) > rateLimitWindow {
		// Reset counter if we're in a new window
		requestCount = 0
		lastRequestTime = now
	}

	if requestCount >= maxRequestsPerMinute {
		// Calculate time until next window
		timeToWait := rateLimitWindow - now.Sub(lastRequestTime)
		log.Warnf("Rate limit reached. Please wait %v before trying again.", timeToWait.Round(time.Second))
		return false
	}

	requestCount++
	return true
}

func handleGeminiError(err error, session *discordgo.Session, channelID string) {
	if err == nil {
		return
	}

	errMsg := err.Error()
	if errMsg == "RESOURCE_EXHAUSTED" || errMsg == "429" {
		log.Warn("Rate limit exceeded for Gemini API")
		_, _ = session.ChannelMessageSend(channelID, "I'm currently experiencing high demand. Please try again in a minute.")
	} else {
		log.Errorf("Gemini API error: %v", err)
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I encountered an error while processing your request. Please try again later.")
	}
}

func chatbot(session *discordgo.Session, userID string, channelID string, userMessageContent string) {
	if geminiClient == nil {
		log.Error("Gemini client is not initialized.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		return
	}

	if !checkRateLimit() {
		_, _ = session.ChannelMessageSend(channelID, "I'm currently experiencing high demand. Please try again in a minute.")
		return
	}

	ctx := context.Background()

	if userMessageContent == "" {
		log.Info("User message content is empty. Nothing to send to AI.")
		return
	}

	_ = session.ChannelTyping(channelID)

	log.Infof("Sending user message to AI: '%s'", userMessageContent)

	// Check if we need to start a new chat session
	chatSession, exists := chatSessions[channelID]
	if !exists {
		chat, err := geminiClient.Chats.Create(ctx, TextModelName, &genai.GenerateContentConfig{Tools: ReminderTools}, []*genai.Content{
			{
				Parts: []*genai.Part{genai.NewPartFromText(SystemInstruction)},
				Role:  genai.RoleUser,
			},
		})
		if err != nil {
			log.Errorf("Failed to create chat session: %v", err)
			handleGeminiError(err, session, channelID)
			return
		}
		chatSession = chat
		chatSessions[channelID] = chatSession
	}

	// Get chat history for context
	history := chatSession.History(false) // Get full history
	log.Infof("Chat history length: %d", len(history))

	resp, err := chatSession.SendMessage(ctx, genai.Part{Text: userMessageContent})
	if err != nil {
		log.Errorf("Error getting response from AI: %v", err)
		handleGeminiError(err, session, channelID)
		return
	}

	// Robust function call handling loop
	for {
		respText := resp.Text()
		if respText != "" {
			// Got a text reply, send to user
			_, err = session.ChannelMessageSend(channelID, respText)
			if err != nil {
				log.Errorf("Error sending message to Discord: %v", err)
			}
			return
		}

		// Check for function call
		var fc *genai.FunctionCall
		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			fc = resp.Candidates[0].Content.Parts[0].FunctionCall
		}
		if fc == nil {
			log.Error("No text or function call in response")
			_, _ = session.ChannelMessageSend(channelID, "Sorry, I received an empty response from the AI service.")
			return
		}
		log.Infof("Handling function call: %s", fc.Name)
		part, err := HandleFunctionCallWithContext(session, nil, fc, userID, channelID)
		if err != nil {
			log.Errorf("Error handling function call: %v", err)
			handleGeminiError(err, session, channelID)
			return
		}
		log.Infof("Function call '%s' result: %+v", fc.Name, part)
		// Send the function response back to the model and continue the loop
		resp, err = chatSession.SendMessage(ctx, *part)
		if err != nil {
			log.Errorf("Error sending function response to AI: %v", err)
			handleGeminiError(err, session, channelID)
			return
		}
	}
}
