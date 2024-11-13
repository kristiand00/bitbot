package bot

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
)

const (
	maxTokens         = 1500
	maxContextTokens  = 2000
	maxMessageTokens  = 2000
	systemMessageText = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain."
)

var lastChannelID string // Track the last used channelID globally

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
	maxTokens := 1500
	totalTokens := 0

	// Track existing message content to prevent duplicates
	existingContents := make(map[string]bool)
	for _, msg := range conversationHistory {
		content, ok := msg["content"].(string)
		if ok {
			existingContents[content] = true
			tokens := len(content) + 2 // Account for message tokens
			totalTokens += tokens
		}
	}

	// Process the recent messages in reverse order (newest to oldest)
	for _, message := range messages {
		// Skip messages older than 30 minutes
		if time.Since(message.Timestamp) > 30*time.Minute {
			continue
		}

		// Skip if the message content already exists in conversationHistory
		if existingContents[message.Content] {
			continue
		}

		// Calculate tokens for the message content
		tokens := len(message.Content) + 2
		if totalTokens+tokens <= maxTokens {
			// Add message to history and mark its content as added
			conversationHistory = append([]map[string]interface{}{{
				"role":    "user",
				"content": message.Content,
			}}, conversationHistory...)
			totalTokens += tokens
			existingContents[message.Content] = true
			log.Infof("Adding message with tokens: %d", tokens)
		} else {
			break // Stop if adding the next message would exceed the max token limit
		}
	}

	// Trim older messages if needed to stay within maxTokens
	for totalTokens > maxTokens && len(conversationHistory) > 1 {
		firstMessage := conversationHistory[0]
		content, ok := firstMessage["content"].(string)
		if ok {
			totalTokens -= len(content) + 2
		}
		conversationHistory = conversationHistory[1:]
		log.Infof("Trimming oldest message to maintain token limit, remaining tokens: %d", totalTokens)
	}

	log.Infof("Final Conversation History Order (Total Tokens: %d): %v", totalTokens, conversationHistory)
	return conversationHistory
}




// Function to handle Groq API requests and pagination
func chatGPT(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) {
	OpenAIToken := OpenAIToken
	GroqBaseURL := "https://api.groq.com/openai/v1"
	GroqModel := "llama-3.2-90b-text-preview"

	// Add system message at the start of conversation history
	conversationHistory = append([]map[string]interface{}{
		{"role": "system", "content": systemMessageText},
	}, conversationHistory...)

	client := http.Client{}
	requestBody, err := json.Marshal(map[string]interface{}{
		"model":             GroqModel,
		"messages":          conversationHistory,
		"max_tokens":        maxTokens,
		"frequency_penalty": 0.3,
		"presence_penalty":  0.6,
	})
	if err != nil {
		log.Errorf("Failed to marshal request body: %v", err)
		return
	}

	req, err := http.NewRequest("POST", GroqBaseURL+"/chat/completions", bytes.NewBuffer(requestBody))
	if err != nil {
		log.Errorf("Failed to create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+OpenAIToken)

	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Failed to make request: %v", err)
		return
	}
	defer resp.Body.Close()

	var groqResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Failed to read response body: %v", err)
		return
	}

	err = json.Unmarshal(body, &groqResp)
	if err != nil {
		log.Errorf("Failed to decode response: %v", err)
		return
	}

	if len(groqResp.Choices) > 0 {
		gptResponse := groqResp.Choices[0].Message.Content
		_, err := session.ChannelMessageSend(channelID, gptResponse)
		if err != nil {
			log.Error("Error sending message:", err)
			return
		}
	}
}
