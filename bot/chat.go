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
	lastChannelID     string
	geminiClient      *genai.Client // Corrected type
	geminiChatModel   *genai.GenerativeModel
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

func populateConversationHistory(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) []map[string]interface{} {
	if lastChannelID != channelID {
		conversationHistory = nil
		lastChannelID = channelID
		log.Infof("New channel detected. Resetting conversation history.")
	}
	discordMessages, err := session.ChannelMessages(channelID, 20, "", "", "")
	if err != nil {
		log.Error("Error retrieving channel history:", err)
		return conversationHistory
	}
	maxTokensInHistory := 1500
	totalTokens := 0
	existingContents := make(map[string]bool)
	for _, msg := range conversationHistory {
		content, ok := msg["content"].(string)
		if ok {
			existingContents[content] = true
			tokens := len(content)
			totalTokens += tokens
		}
	}
	var tempHistory []map[string]interface{}
	for i := len(discordMessages) - 1; i >= 0; i-- {
		message := discordMessages[i]
		if time.Since(message.Timestamp) > 30*time.Minute {
			continue
		}
		if existingContents[message.Content] {
			continue
		}
		if session.State != nil && session.State.User != nil && message.Author.ID == session.State.User.ID {
			continue
		}
		tokens := len(message.Content)
		if totalTokens+tokens <= maxTokensInHistory {
			tempHistory = append(tempHistory, map[string]interface{}{
				"role":    "user",
				"content": message.Content,
			})
			totalTokens += tokens
			existingContents[message.Content] = true
		} else {
			break
		}
	}
    for i, j := 0, len(tempHistory)-1; i < j; i, j = i+1, j-1 {
        tempHistory[i], tempHistory[j] = tempHistory[j], tempHistory[i]
    }
    conversationHistory = append(conversationHistory, tempHistory...)
	finalTrimmedHistory := []map[string]interface{}{}
	currentTotalTokens := 0
    for i := len(conversationHistory) - 1; i >= 0; i-- {
        msg := conversationHistory[i]
        content, ok := msg["content"].(string)
        if !ok { continue }
        tokens := len(content)
        if currentTotalTokens+tokens <= maxTokensInHistory {
            finalTrimmedHistory = append([]map[string]interface{}{msg}, finalTrimmedHistory...)
            currentTotalTokens += tokens
        } else {
            break
        }
    }
     for i, j := 0, len(finalTrimmedHistory)-1; i < j; i, j = i+1, j-1 {
        finalTrimmedHistory[i], finalTrimmedHistory[j] = finalTrimmedHistory[j], finalTrimmedHistory[i]
    }
    conversationHistory = finalTrimmedHistory
	log.Infof("Final Conversation History (Tokens: %d): %d messages", currentTotalTokens, len(conversationHistory))
	return conversationHistory
}

func chatGPT(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) {
	if geminiClient == nil || geminiChatModel == nil {
		log.Error("Gemini client or model is not initialized.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		return
	}

	ctx := context.Background()
	// chatContents changed from []*genai.Content to []genai.Part
	chatParts := []genai.Part{}
	// System instruction is now set globally for the model,
	// but chat history needs to be passed as Parts.
	// The genai.Content struct has Role and Parts.
	// We will build a history of genai.Part, assuming the roles are handled by the ChatSession or the way parts are added.
	// For direct GenerateContent, we might need to construct genai.Content items if roles need to be explicit per message.
	// However, GenerateContent on GenerativeModel takes ...Part, implying a sequence of Parts.
	// Let's adapt to passing parts directly. If conversation needs explicit roles for each message part,
	// this might need further adjustment based on how genai.GenerativeModel.GenerateContent processes parts.
	// The documentation for GenerativeModel.GenerateContent(ctx, parts ...Part) suggests it takes a sequence of parts.
	// For a chat-like structure, this usually means alternating user/model content.
	// The current structure of conversationHistory (map[string]interface{}{"role": ..., "content": ...})
	// will be converted to a flat list of genai.Part. This might lose explicit role information if not handled by the SDK implicitly.
	// Let's assume for now that the SDK handles alternating roles or that the prior setup of SystemInstruction covers the bot's role.
	// This is a potential area for bugs if the SDK expects genai.Content with roles for chat history.
	// The ChatSession object in the SDK is designed for this, but we are using GenerateContent directly.

	var currentChatSession *genai.ChatSession
	if geminiChatModel != nil {
		currentChatSession = geminiChatModel.StartChat()
		currentChatSession.History = []*genai.Content{} // Initialize history

		for _, msgData := range conversationHistory {
			content, okContent := msgData["content"].(string)
			roleStr, okRole := msgData["role"].(string)
			if !okContent || !okRole {
				log.Warnf("Skipping message in history: missing content or role.")
				continue
			}
			// Add to ChatSession history
			currentChatSession.History = append(currentChatSession.History, &genai.Content{Role: roleStr, Parts: []genai.Part{genai.Text(content)}})
			// Also prepare parts for GenerateContent if needed, though SendMessage from ChatSession is preferred.
			chatParts = append(chatParts, genai.Text(content))
		}
	} else {
		log.Error("geminiChatModel is nil, cannot start chat session or prepare history.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat model is not available.")
		return
	}

	if len(currentChatSession.History) == 0 {
		log.Info("Conversation history is empty for ChatSession. Replying with greeting.")
		_, _ = session.ChannelMessageSend(channelID, "Hello! How can I help you today? (History was empty for ChatSession)")
		return
	}

	_ = session.ChannelTyping(channelID)

	// Use ChatSession's SendMessage instead of direct GenerateContent for chat
	resp, err := currentChatSession.SendMessage(ctx, chatParts[len(chatParts)-1]) // Send only the last message part

	// If not using ChatSession, GenerateContent would be:
	// resp, err := geminiChatModel.GenerateContent(ctx, chatParts...)
	// The switch to ChatSession.SendMessage is a significant change.
	// It expects only the new message parts, as history is managed by the session.

	if err != nil {
		log.Errorf("Failed to generate content with Gemini: %v", err)
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I encountered an error trying to get a response.")
		return
	}

	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil && len(resp.Candidates[0].Content.Parts) > 0 {
		if textPart, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
			gptResponse := string(textPart)
			_, _ = session.ChannelMessageSend(channelID, gptResponse)
		} else {
			log.Warn("Gemini response part is not genai.Text.")
			_, _ = session.ChannelMessageSend(channelID, "Sorry, I received an unexpected response format.")
		}
	} else {
		log.Warn("Received no content or empty message from Gemini.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I didn't receive a response. You could try rephrasing.")
	}
}
