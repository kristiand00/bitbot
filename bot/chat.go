package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	SystemInstruction = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain."
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

	// Test the model availability
	log.Info("Starting model availability tests...")
	if err := testModelAvailability(ctx); err != nil {
		log.Warnf("Failed to test model availability: %v", err)
	}

	log.Infof("GenAI Client initialization completed in %v", time.Since(startTime))
	return nil
}

func testModelAvailability(ctx context.Context) error {
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

	// Test audio model with Live API
	log.Info("Testing audio model availability with Live API...")
	audioTestStart := time.Now()

	// Create Live API request
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?key=%s", AudioModelName, geminiAPIKey)

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{
						"text": "test audio",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %v", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", geminiAPIKey)

	// Send request
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("audio model test failed with status %d: %s", resp.StatusCode, string(body))
	}

	log.Infof("Audio model %s is available via Live API (test took %v)", AudioModelName, time.Since(audioTestStart))
	log.Infof("All model availability tests completed in %v", time.Since(startTime))
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

// getGeminiTextResponseForVoice function REMOVED

// prepareMessageForHistory converts a message content and role into *genai.Content
// and appends it to the existing history.
func prepareMessageForHistory(messageContent string, messageRole string, existingHistory []*genai.Content) []*genai.Content {
	if messageRole != "user" && messageRole != "model" {
		log.Warnf("Invalid message role: %s. Role must be 'user' or 'model'.", messageRole)
		return existingHistory
	}
	newMessage := &genai.Content{
		Parts: []*genai.Part{genai.NewPartFromText(messageContent)},
		Role:  messageRole,
	}
	updatedHistory := append(existingHistory, newMessage)
	return updatedHistory
}

func chatGPT(session *discordgo.Session, channelID string, userMessageContent string) {
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
		chat, err := geminiClient.Chats.Create(ctx, TextModelName, nil, []*genai.Content{
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

	// Send the message and get response
	resp, err := chatSession.SendMessage(ctx, genai.Part{Text: userMessageContent})
	if err != nil {
		log.Errorf("Error getting response from AI: %v", err)
		handleGeminiError(err, session, channelID)
		return
	}

	// Extract the response text
	respText := resp.Text()
	if respText == "" {
		log.Error("Received empty response from AI")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I received an empty response from the AI service.")
		return
	}

	// Send the response back to Discord
	_, err = session.ChannelMessageSend(channelID, respText)
	if err != nil {
		log.Errorf("Error sending message to Discord: %v", err)
	}
}

// processTranscribedVoiceInput function REMOVED
