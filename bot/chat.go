package bot

import (
	"context"
	"github.com/bwmarrin/discordgo"
	openai "github.com/sashabaranov/go-openai"
)

func chatGPT(message string) *discordgo.MessageSend {
	client := openai.NewClient(OpenAIToken)
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			MaxTokens:        800,
			FrequencyPenalty: 0.3,
			PresencePenalty:  0.6,
			Model:            openai.GPT3Dot5Turbo0301,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: "You are an amazing and versatile person! As an ethical hacker, coder, polyglot, and poet.",
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: message,
				},
			},
		},
	)

	if err != nil {
		return &discordgo.MessageSend{
			Content: "Sorry, there was an error trying to connect to api",
		}
	}

	gptResponse := (resp.Choices[0].Message.Content)

	embed := &discordgo.MessageSend{
		Content: gptResponse,
	}
	return embed
}
