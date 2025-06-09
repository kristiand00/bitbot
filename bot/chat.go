package bot

import (
	"context" // Added
	"fmt"     // Added
	"time"    // Keep: used by populateConversationHistory

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"

	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googleai" // For Gemini
)

const (
	maxTokens         = 1500 // Preserved
	maxContextTokens  = 2000 // Preserved (though not directly used by new chatGPT, kept for now)
	maxMessageTokens  = 2000 // Preserved (though not directly used by new chatGPT, kept for now)
	systemMessageText = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain." // Preserved
)

var lastChannelID string // Track the last used channelID globally - Preserved
var geminiModel *genkit.Model // Added

// InitGenkit initializes the Genkit library and Gemini model.
// It will be called from bot.go after the API key is loaded.
// Assumes GOOGLE_API_KEY environment variable will be set by the caller using bot.GeminiAPIKey.
func InitGenkit() error {
	// Initialize the Google AI plugin for Genkit.
	// This typically relies on the GOOGLE_API_KEY environment variable.
	// Ensure bot.GeminiAPIKey is set to os.Setenv("GOOGLE_API_KEY", bot.GeminiAPIKey) before this.
	if err := googleai.Init(context.Background()); err != nil {
		log.Errorf("Failed to initialize Google AI plugin for Genkit: %v", err)
		return fmt.Errorf("failed to initialize Google AI plugin: %w", err)
	}

	geminiModel = googleai.Model("gemini-1.5-flash-latest")
	if geminiModel == nil {
		log.Error("Failed to get Gemini model 'gemini-1.5-flash-latest' from Genkit.")
		return fmt.Errorf("failed to get model 'gemini-1.5-flash-latest'")
	}

	log.Info("Genkit and Gemini model 'gemini-1.5-flash-latest' initialized successfully via googleai plugin.")
	return nil
}

func populateConversationHistory(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) []map[string]interface{} {
	// Reset conversationHistory if this is a new channel
	if lastChannelID != channelID {
		conversationHistory = nil
		lastChannelID = channelID
		log.Infof("New channel detected. Resetting conversation history.")
	}

	// Retrieve recent messages only from the specified channel
	messages, err := session.ChannelMessages(channelID, 20, "", "", "")
	if err != nil {
		log.Error("Error retrieving channel history:", err)
		return conversationHistory
	}

	// Set max tokens and initialize counters
	// Note: maxTokens constant is used directly in the new chatGPT, this local variable is fine for this func
	maxTokensInHistory := 1500 // Using a local variable to avoid conflict if global maxTokens is used differently
	totalTokens := 0

	// Track existing message content to prevent duplicates
	existingContents := make(map[string]bool)
	for _, msg := range conversationHistory {
		content, ok := msg["content"].(string)
		if ok {
			existingContents[content] = true
			// Simple token estimation (chars), real tokenization is more complex
			tokens := len(content)
			totalTokens += tokens
		}
	}

	// Process the recent messages in reverse order (newest to oldest)
	// to build up to the token limit with the most recent messages.
	var tempHistory []map[string]interface{}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		// Skip messages older than 30 minutes
		if time.Since(message.Timestamp) > 30*time.Minute {
			continue
		}

		// Skip if the message content already exists in conversationHistory (from previous runs or this session)
		if existingContents[message.Content] {
			continue
		}

		// Skip bot's own messages by checking author (if session.State.User is populated and matches message.Author)
		// or by checking if the message content is one of the bot's known responses (harder).
		// For now, we assume populateConversationHistory is called with user messages.
		// If bot messages are in `messages` from discord, they will be added with "user" role here.

		tokens := len(message.Content) // Simple token estimation
		if totalTokens+tokens <= maxTokensInHistory {
			tempHistory = append(tempHistory, map[string]interface{}{
				"role":    "user", // All messages from Discord history are treated as "user" for now
				"content": message.Content,
			})
			totalTokens += tokens
			existingContents[message.Content] = true // Mark as added to avoid re-adding if it appeared multiple times in history
		} else {
			// If adding this message would exceed, we might have space for a smaller one if we continue,
			// but typically we want the most recent contiguous block. So, break.
			break
		}
	}
    // Reverse tempHistory to get chronological order (oldest to newest) before prepending to main history
    for i := len(tempHistory)/2 - 1; i >= 0; i-- {
        opp := len(tempHistory) - 1 - i
        tempHistory[i], tempHistory[opp] = tempHistory[opp], tempHistory[i]
    }
    conversationHistory = append(tempHistory, conversationHistory...)


	// Final trim if somehow totalTokens exceeded (e.g. initial conversationHistory was large)
    // This part ensures existing history + new additions don't exceed the limit.
	finalTrimmedHistory := []map[string]interface{}{}
	currentTotalTokens := 0
    // Iterate from newest to oldest to keep most recent messages
    for i := len(conversationHistory) - 1; i >= 0; i-- {
        msg := conversationHistory[i]
        content, ok := msg["content"].(string)
        if !ok {
            continue // Should not happen if history is well-formed
        }
        tokens := len(content)
        if currentTotalTokens+tokens <= maxTokensInHistory {
            // Prepend to keep chronological order in finalTrimmedHistory
            finalTrimmedHistory = append([]map[string]interface{}{msg}, finalTrimmedHistory...)
            currentTotalTokens += tokens
        } else {
            // History (from newest) already meets token limit
            break
        }
    }
    conversationHistory = finalTrimmedHistory
	log.Infof("Final Conversation History (Total Estimated Tokens: %d): %d messages", currentTotalTokens, len(conversationHistory))
	return conversationHistory
}

func chatGPT(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) {
	if geminiModel == nil {
		log.Error("Gemini model is not initialized. Call InitGenkit first.")
		_, err := session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		if err != nil {
			log.Error("Error sending Discord message for uninitialized model: ", err)
		}
		return
	}

	// Convert conversationHistory to genkit.MessageHistory format
	messages := []*genkit.Message{
		genkit.NewSystemMessage(systemMessageText), // systemMessageText is a const in this file
	}
	for _, msgData := range conversationHistory {
		content, okContent := msgData["content"].(string)
		roleStr, okRole := msgData["role"].(string)

		if !okContent || !okRole {
			log.Warnf("Skipping message in history due to missing content or role: %+v", msgData)
			continue
		}

		// populateConversationHistory currently only adds "user" roles from Discord messages.
		if roleStr == "user" {
			messages = append(messages, genkit.NewUserMessage(content))
		} else if roleStr == "assistant" || roleStr == "model" {
			// This case handles if the bot's own previous replies were somehow stored in conversationHistory
			messages = append(messages, genkit.NewModelMessage(content))
		}
		// Other roles (like "system" from history) are ignored as we add one definitive system message.
	}

	// Create a request for Genkit
	req := &genkit.GenerateRequest{
		Model:    geminiModel,
		Messages: messages,
		Config: &genkit.GenerationConfig{
			MaxOutputTokens: int32(maxTokens), // maxTokens is a const (1500) in this file
			// Add other config like Temperature if needed: Temperature: 0.7,
		},
	}

	// Indicate bot is typing
	_ = session.ChannelTyping(channelID)

	// Generate content using Genkit
	resp, err := genkit.Generate(context.Background(), req)
	if err != nil {
		log.Errorf("Failed to generate content with Genkit/Gemini: %v", err)
		_, sendErr := session.ChannelMessageSend(channelID, "Sorry, I encountered an error trying to get a response.")
		if sendErr != nil {
			log.Error("Error sending Discord error message: ", sendErr)
		}
		return
	}

	if len(resp.Candidates) > 0 && resp.Candidates[0].Message != nil && resp.Candidates[0].Message.Content != "" {
		gptResponse := resp.Candidates[0].Message.Content
		_, err := session.ChannelMessageSend(channelID, gptResponse)
		if err != nil {
			log.Error("Error sending Genkit response to Discord:", err)
			return
		}
	} else {
		log.Warn("Received no content or empty message from Genkit/Gemini.")
		_, sendErr := session.ChannelMessageSend(channelID, "Sorry, I didn't receive a response. You could try rephrasing.")
		if sendErr != nil {
			log.Error("Error sending Discord 'no response' message: ", sendErr)
		}
	}
}
