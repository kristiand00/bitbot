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
	maxTokens         = 2000
	maxContextTokens  = 2000
	maxMessageTokens  = 2000
	systemMessageText = "your name is !bit you are a discord bot, you use brief answers untill asked to elaborate or explain"
)

func populateConversationHistory(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) []map[string]interface{} {
	// Retrieve recent messages from the Discord channel
	messages, err := session.ChannelMessages(channelID, 20, "", "", "")
	if err != nil {
		log.Error("Error retrieving channel history:", err)
		return conversationHistory
	}

	// Define max tokens for the conversation history
	maxTokens := 2000
	totalTokens := 0

	// Calculate current token count in conversation history
	for _, msg := range conversationHistory {
		content, okContent := msg["Content"].(string)
		role, okRole := msg["Role"].(string)
		if okContent && okRole {
			tokens := len(content) + len(role) + 2 // Account for tokens in content and role
			totalTokens += tokens
			log.Infof("Existing message tokens: %d", tokens)
		}
	}

	// Process messages in reverse order (newest to oldest)
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]

		// Check if the message is older than 30 minutes
		if time.Since(message.Timestamp) < 30*time.Minute {
			tokens := len(message.Content) + 2
			if totalTokens+tokens <= maxTokens {
				// Append as map[string]interface{} instead of map[string]string
				conversationHistory = append(conversationHistory, map[string]interface{}{
					"role":    "user",
					"content": message.Content,
				})
				totalTokens += tokens
				log.Infof("Adding message with tokens: %d", tokens)
			} else {
				log.Warnf("Skipping message with tokens: %d", tokens)
			}
		} else {
			log.Infof("Skipping message, older than 30 minutes: %s", message.Content)
		}

		// Ensure the current message is included (regardless of token limit)
		conversationHistory = append(conversationHistory, map[string]interface{}{
			"role":    "user",
			"content": message.Content,
		})
		totalTokens += len(message.Content) + 2
		log.Infof("Adding message with tokens: %d", totalTokens)

		// Now check if the token limit is exceeded and trim older messages
		if totalTokens > maxTokens && len(conversationHistory) > 1 {
			// Remove the oldest message from the history
			conversationHistory = conversationHistory[1:]
			content, okContent := conversationHistory[0]["Content"].(string)
			role, okRole := conversationHistory[0]["Role"].(string)
			if okContent && okRole {
				tokens := len(content) + len(role) + 2
				totalTokens -= tokens
				log.Infof("Trimming message with tokens: %d", tokens)
			}
			log.Info("Trimming oldest message to maintain token limit")
		}
	}

	log.Info("Final Conversation History Order: %s", conversationHistory)
	return conversationHistory
}

// Function to handle Groq API requests and pagination
func chatGPT(session *discordgo.Session, channelID string, conversationHistory []map[string]interface{}) {
	OpenAIToken := OpenAIToken
	GroqBaseURL := "https://api.groq.com/openai/v1"
	GroqModel := "llama-3.1-70b-versatile"

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
