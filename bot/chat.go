package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/google/generative-ai-go/genai" // Correct SDK import
	"google.golang.org/api/option"
)

const (
	systemMessageText = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain."
)

var (
	lastChannelID      string
	geminiClient       *genai.Client // Corrected type
	geminiChatModel    *genai.GenerativeModel
	currentChatSession *genai.ChatSession // Global chat session
)

func InitGeminiClient(apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("Gemini API key is not provided")
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey)) // Corrected function
	if err != nil {
		log.Errorf("Failed to create Generative Client: %v", err)
		return fmt.Errorf("failed to create Generative Client: %w", err)
	}
	geminiClient = client
	geminiChatModel = geminiClient.GenerativeModel("gemini-1.5-flash-latest")
	geminiChatModel.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemMessageText)},
	}
	log.Info("Generative Client initialized successfully with model 'gemini-1.5-flash-latest'.")
	return nil
}

// prepareMessageForHistory converts a message content and role into *genai.Content
// and appends it to the existing history.
func prepareMessageForHistory(messageContent string, messageRole string, existingHistory []*genai.Content) []*genai.Content {
	// Validate role
	if messageRole != "user" && messageRole != "model" {
		log.Warnf("Invalid message role: %s. Role must be 'user' or 'model'.", messageRole)
		// Decide how to handle invalid role: return existing history, or error, or default role
		return existingHistory // Or handle error appropriately
	}

	// Create new message content
	newMessage := &genai.Content{
		Parts: []genai.Part{genai.Text(messageContent)},
		Role:  messageRole,
	}

	// Append to existing history
	updatedHistory := append(existingHistory, newMessage)

	return updatedHistory
}

func chatGPT(session *discordgo.Session, channelID string, userMessageContent string) {
	if geminiClient == nil || geminiChatModel == nil {
		log.Error("Gemini client or model is not initialized.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		return
	}

	ctx := context.Background()

	if geminiChatModel == nil {
		log.Error("Gemini model is not initialized. Cannot proceed.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat model is not available.")
		return
	}

	// Handle session initialization based on channelID
	if lastChannelID != channelID || currentChatSession == nil {
		if currentChatSession == nil {
			log.Infof("currentChatSession is nil. Initializing new chat session for channel %s.", channelID)
		} else {
			log.Infof("Channel ID changed from %s to %s. Initializing new chat session.", lastChannelID, channelID)
		}
		currentChatSession = geminiChatModel.StartChat()
		currentChatSession.History = []*genai.Content{} // Start with a fresh history
		lastChannelID = channelID
		log.Info("Fetching last 20 messages to populate history...")
		discordMessages, err := session.ChannelMessages(channelID, 20, "", "", "")
		if err != nil {
			log.Errorf("Failed to fetch channel messages for history: %v", err)
			// Depending on policy, might want to return or continue with empty history
		} else {
			// Iterate in reverse to get oldest messages first for correct history order
			for i := len(discordMessages) - 1; i >= 0; i-- {
				msg := discordMessages[i]
				// Skip bot's own messages if they were added via a different mechanism or to avoid loops
				// For now, we determine role based on author.
				// If session.State.User is nil, this check might panic.
				var role string
				if session.State != nil && session.State.User != nil && msg.Author.ID == session.State.User.ID {
					role = "model" // Message from the bot itself
				} else {
					role = "user" // Message from another user
				}
				// Filter out empty messages or other specific conditions if needed
				if msg.Content == "" {
					continue
				}
				currentChatSession.History = prepareMessageForHistory(msg.Content, role, currentChatSession.History)
			}
			log.Infof("Populated session history with %d messages from Discord. Current history length: %d", len(discordMessages), len(currentChatSession.History))
		}
	} else {
		log.Infof("Continuing existing chat session for channel %s. Current history length: %d", channelID, len(currentChatSession.History))
		// For ongoing sessions, the history is already in currentChatSession.
		// The new userMessageContent will be sent directly via SendMessage.
	}

	// Current user's message to be sent
	if userMessageContent == "" {
		log.Info("User message content is empty. Nothing to send to AI.")
		// Optionally, send a message to Discord user that an empty message was ignored.
		return
	}
	currentUserParts := []genai.Part{genai.Text(userMessageContent)}

	_ = session.ChannelTyping(channelID)

	log.Infof("Sending user message to AI: '%s'. Current history length before SendMessage: %d.",
		userMessageContent, len(currentChatSession.History))

	// SendMessage will append the user message (currentUserParts) and then the AI's response to currentChatSession.History
	resp, err := currentChatSession.SendMessage(ctx, currentUserParts...)

	if err != nil {
		log.Errorf("Failed to send message via ChatSession: %v", err)
		// Consider if session needs reset: currentChatSession = nil
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I encountered an error trying to get a response.")
		return
	}

	// Process and send the response
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil && len(resp.Candidates[0].Content.Parts) > 0 {
		// The AI's response is automatically added to currentChatSession.History by the SendMessage call.
		aiResponseContent := resp.Candidates[0].Content
		log.Infof("AI response received. Role: %s. Current history length after SendMessage: %d", aiResponseContent.Role, len(currentChatSession.History))

		if textPart, ok := aiResponseContent.Parts[0].(genai.Text); ok {
			gptResponse := string(textPart)
			_, err := session.ChannelMessageSend(channelID, gptResponse)
			if err != nil {
				log.Errorf("Failed to send AI response to Discord: %v", err)
			}
		} else {
			log.Warn("Gemini response part is not genai.Text.")
			_, _ = session.ChannelMessageSend(channelID, "Sorry, I received an unexpected response format.")
		}
	} else {
		log.Warn("Received no content or empty message from Gemini.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I didn't receive a response. You could try rephrasing.")
	}
}

// Helper function to convert genai.Part slice to string for logging
func messagePartToString(parts []genai.Part) string {
	if len(parts) == 0 {
		return ""
	}
	if textPart, ok := parts[0].(genai.Text); ok {
		return string(textPart)
	}
	return "[non-text part]"
}
