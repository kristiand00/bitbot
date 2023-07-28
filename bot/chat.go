package bot

import (
	"context"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	openai "github.com/sashabaranov/go-openai"
)

const maxTokens = 3000
const maxContextTokens = 4097

func chatGPT(message string, conversationHistory []openai.ChatCompletionMessage) *discordgo.MessageSend {
	client := openai.NewClient(OpenAIToken)

	// Calculate the total number of tokens used in the conversation history and completion.
	totalTokens := 0
	for _, msg := range conversationHistory {
		totalTokens += len(msg.Content) + len(msg.Role) + 2 // Account for role and content tokens, plus two extra for delimiters.
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
			if totalTokens-len(conversationHistory[len(conversationHistory)-1].Content)-len(conversationHistory[len(conversationHistory)-1].Role)-2 <= maxTokens {
				totalTokens -= len(conversationHistory[len(conversationHistory)-1].Content) + len(conversationHistory[len(conversationHistory)-1].Role) + 2
				conversationHistory = conversationHistory[:len(conversationHistory)-1]
			} else {
				// Otherwise, remove the first message from the conversation history.
				totalTokens -= len(conversationHistory[0].Content) + len(conversationHistory[0].Role) + 2
				conversationHistory = conversationHistory[1:]
			}
		}
	}

	// Add a system message at the beginning of the conversation history with the instructions.
	systemMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "1. Identify the key points or main ideas of the original answers.\n2. Summarize each answer using concise and informative language.\n3. Prioritize clarity and brevity, capturing the essence of the information provided.\n4. Trim down unnecessary details and avoid elaboration.\n5. Make sure the summarized answers still convey accurate and meaningful information.",
	}

	// Combine the system message, previous conversation history, and the current user message.
	messages := append([]openai.ChatCompletionMessage{systemMessage}, conversationHistory...)
	messages = append(messages, openai.ChatCompletionMessage{
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
