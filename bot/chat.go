package bot

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	openai "github.com/sashabaranov/go-openai"
)

const (
	maxTokens         = 3000
	maxContextTokens  = 4097
	maxMessageTokens  = 1000
	systemMessageText = "1. Identify the key points or main ideas of the original answers.\n2. Summarize each answer using concise and informative language.\n3. Prioritize clarity and brevity, capturing the essence of the information provided.\n4. Trim down unnecessary details and avoid elaboration.\n5. Make sure the summarized answers still convey accurate and meaningful information."
)

func populateConversationHistory(session *discordgo.Session, channelID string, conversationHistory []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	messages, err := session.ChannelMessages(channelID, 100, "", "", "")
	if err != nil {
		log.Error("Error retrieving channel history:", err)
		return conversationHistory
	}

	totalTokens := 0
	for _, msg := range conversationHistory {
		totalTokens += len(msg.Content) + len(msg.Role) + 2
	}

	maxHistoryTokens := maxTokens - totalTokens

	for _, message := range messages {
		if message.Author.ID == session.State.User.ID {
			continue // Skip the bot's own messages
		}

		if len(message.Content) > 0 {
			tokens := len(message.Content) + 2 // Account for role and content tokens
			if totalTokens+tokens <= maxContextTokens && len(conversationHistory) < maxHistoryTokens {
				conversationHistory = append(conversationHistory, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: message.Content,
				})
				totalTokens += tokens
			} else {
				if totalTokens+tokens > maxContextTokens {
					log.Warn("Message token count exceeds maxContextTokens:", len(message.Content), len(message.Content)+2)
				} else {
					log.Warn("Conversation history length exceeds maxContextTokens:", len(conversationHistory), maxHistoryTokens)
				}
				break
			}
		}
	}

	return conversationHistory
}

func chatGPT(session *discordgo.Session, channelID string, message string, conversationHistory []openai.ChatCompletionMessage) *discordgo.MessageSend {
	conversationHistory = populateConversationHistory(session, channelID, conversationHistory)

	client := openai.NewClient(OpenAIToken)

	// Calculate the total tokens in the conversation history
	totalTokens := 0
	for _, msg := range conversationHistory {
		totalTokens += len(msg.Content) + len(msg.Role) + 2
	}

	log.Info("Total tokens in conversation history:", totalTokens)

	// Calculate the tokens in the completion message
	completionTokens := len(message)
	log.Info("Tokens in completion message:", completionTokens)

	// Calculate the total tokens including the new message
	totalMessageTokens := len(message) + 2 // Account for role and content tokens

	// Ensure the total tokens of messages including new message doesn't exceed maxMessageTokens
	for totalTokens+totalMessageTokens > maxMessageTokens {
		tokensToRemove := totalTokens + totalMessageTokens - maxMessageTokens
		tokensRemoved := 0
		trimmedMessages := []openai.ChatCompletionMessage{} // Store trimmed messages
		for _, msg := range conversationHistory {
			tokens := len(msg.Content) + len(msg.Role) + 2
			if tokensRemoved+tokens <= tokensToRemove {
				tokensRemoved += tokens
				log.Info("Removing message with tokens:", tokens)
			} else {
				trimmedMessages = append(trimmedMessages, msg)
			}
		}
		if tokensRemoved > 0 {
			conversationHistory = trimmedMessages
			totalTokens -= tokensRemoved
		} else {
			break
		}
	}

	// Add user message to conversation history
	userMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message,
	}
	conversationHistory = append(conversationHistory, userMessage)

	// Construct system message
	systemMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemMessageText,
	}

	// Combine messages, ensuring they don't exceed maxTokens
	messages := []openai.ChatCompletionMessage{systemMessage}
	totalTokens = len(systemMessage.Content) + len(systemMessage.Role) + 2

	for _, msg := range conversationHistory {
		tokens := len(msg.Content) + len(msg.Role) + 2
		if totalTokens+tokens <= maxTokens {
			messages = append(messages, msg)
			totalTokens += tokens
		} else {
			break
		}
	}

	// Perform GPT-3.5 Turbo completion
	log.Info("Starting GPT-3.5 Turbo completion...")
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			MaxTokens:        maxTokens,
			FrequencyPenalty: 0.3,
			PresencePenalty:  0.6,
			Model:            openai.GPT3Dot5Turbo,
			Messages:         messages,
		},
	)
	log.Info("GPT-3.5 Turbo completion done.")

	// Handle API errors
	if err != nil {
		log.Error("Error connecting to the OpenAI API:", err)
		return &discordgo.MessageSend{
			Content: "Sorry, there was an error trying to connect to the API",
		}
	}

	// Construct and return the bot's response
	gptResponse := resp.Choices[0].Message.Content
	embed := &discordgo.MessageSend{
		Content: gptResponse,
	}
	return embed
}
