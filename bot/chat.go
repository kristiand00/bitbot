package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"google.golang.org/genai"
	"google.golang.org/api/option" // Re-add for option.WithAPIKey
)

// Model name constants
const (
	AudioModelName    = "gemini-2.5-flash-exp-native-audio-thinking-dialog"
	TextModelName     = "gemini-2.5-flash-preview-05-20"
	systemMessageText = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain."
)

var (
	lastChannelID         string
	geminiClient          *genai.Client
	genaiModel            *genai.GenerativeModel // Re-added and correctly typed
	currentChatSession    *genai.Chat
	// userVoiceChatSessions map[string]*genai.ChatSession // REMOVED
)

func InitGeminiClient(apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("Gemini API key is not provided")
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey)) // Corrected client initialization
	if err != nil {
		log.Errorf("Failed to create Generative Client: %v", err)
		return fmt.Errorf("failed to create Generative Client: %w", err)
	}
	geminiClient = client
	genaiModel = client.GenerativeModel(TextModelName) // Initialize genaiModel
	genaiModel.SystemInstruction = &genai.Content{ // Set SystemInstruction on the model
		Parts: []genai.Part{genai.Text(systemMessageText)},
		Role:  genai.RoleModel,
	}
	log.Infof("GenAI Client and Model initialized successfully with text model: %s", TextModelName)
	return nil
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
		Parts: []genai.Part{genai.Text(messageContent)}, // Corrected Part creation
		Role:  messageRole,
	}
	updatedHistory := append(existingHistory, newMessage)
	return updatedHistory
}

func chatGPT(session *discordgo.Session, channelID string, userMessageContent string) {
	if geminiClient == nil || genaiModel == nil { // Corrected check
		log.Error("Gemini client or model is not initialized.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		return
	}

	ctx := context.Background()

	if genaiModel == nil { // Ensure genaiModel is not nil
		log.Error("Gemini model (genaiModel) is not initialized. Cannot proceed.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat model is not available.")
		return
	}

	if lastChannelID != channelID || currentChatSession == nil {
		if currentChatSession == nil {
			log.Infof("currentChatSession is nil. Initializing new chat session for channel %s.", channelID)
		} else {
			log.Infof("Channel ID changed from %s to %s. Initializing new chat session.", lastChannelID, channelID)
		}
		// Use genaiModel.StartChat() which uses SystemInstruction from the model
		currentChatSession = genaiModel.StartChat()
		if currentChatSession.History == nil {
			currentChatSession.History = []*genai.Content{}
		}
		lastChannelID = channelID
		log.Info("Fetching last 20 messages to populate history...")
		discordMessages, err := session.ChannelMessages(channelID, 20, "", "", "")
		if err != nil {
			log.Errorf("Failed to fetch channel messages for history: %v", err)
		} else {
			for i := len(discordMessages) - 1; i >= 0; i-- {
				msg := discordMessages[i]
				var role string
				if session.State != nil && session.State.User != nil && msg.Author.ID == session.State.User.ID {
					role = "model"
				} else {
					role = "user"
				}
				if msg.Content == "" {
					continue
				}
				currentChatSession.History = prepareMessageForHistory(msg.Content, role, currentChatSession.History)
			}
			log.Infof("Populated session history with %d messages from Discord. Current history length: %d", len(discordMessages), len(currentChatSession.History))
		}
	} else {
		log.Infof("Continuing existing chat session for channel %s. Current history length: %d", channelID, len(currentChatSession.History))
	}

	if userMessageContent == "" {
		log.Info("User message content is empty. Nothing to send to AI.")
		return
	}
	currentUserParts := []genai.Part{genai.Text(userMessageContent)} // Corrected Part creation

	_ = session.ChannelTyping(channelID)

	log.Infof("Sending user message to AI: '%s'. Current history length before SendMessage: %d.",
		userMessageContent, len(currentChatSession.History))

	resp, err := currentChatSession.SendMessage(ctx, currentUserParts...)

	if err != nil {
		log.Errorf("Failed to send message via ChatSession: %v", err)
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I encountered an error trying to get a response.")
		return
	}

	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil && len(resp.Candidates[0].Content.Parts) > 0 {
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

// processTranscribedVoiceInput function REMOVED

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
