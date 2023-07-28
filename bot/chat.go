package bot

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	openai "github.com/sashabaranov/go-openai"
)

const maxTokens = 2000
const maxContextTokens = 4097

func chatGPT(message string, conversationHistory []openai.ChatCompletionMessage) *discordgo.MessageSend {
	client := openai.NewClient(OpenAIToken)

	// Calculate the total number of tokens used in the conversation history and completion.
	totalTokens := 0
	for _, msg := range conversationHistory {
		totalTokens += len(msg.Content)
	}

	// Calculate the number of tokens used in the completion.
	completionTokens := len(message)

	// If the total tokens (context + completion) exceed the maxTokens limit, truncate the completion first.
	for totalTokens+completionTokens > maxTokens {
		// Remove tokens from the beginning of the completion message.
		if completionTokens > maxTokens {
			message = message[:maxTokens]
			completionTokens = maxTokens
		} else {
			// If removing the last message reduces the context within the limit, remove it.
			if totalTokens-len(conversationHistory[len(conversationHistory)-1].Content) <= maxTokens {
				totalTokens -= len(conversationHistory[len(conversationHistory)-1].Content)
				conversationHistory = conversationHistory[:len(conversationHistory)-1]
			} else {
				// Otherwise, remove the first message from the conversation history.
				totalTokens -= len(conversationHistory[0].Content)
				conversationHistory = conversationHistory[1:]
			}
		}
	}

	// Combine the previous conversation history with the current user message.
	messages := append(conversationHistory, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message,
	})

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

	if err != nil {
		log.Error("Error connecting to the OpenAI API:", err)
		return &discordgo.MessageSend{
			Content: "Sorry, there was an error trying to connect to the API",
		}
	}

	gptResponse := resp.Choices[0].Message.Content

	embed := &discordgo.MessageSend{
		Content: gptResponse,
	}
	return embed
}
