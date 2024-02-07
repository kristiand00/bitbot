package bot

import (
	"context"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	openai "github.com/sashabaranov/go-openai"
)

const (
	maxTokens         = 2000
	maxContextTokens  = 4000
	maxMessageTokens  = 2000
	systemMessageText = "your name is !bit you are a discord bot"
)

func populateConversationHistory(session *discordgo.Session, channelID string, conversationHistory []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	messages, err := session.ChannelMessages(channelID, 20, "", "", "")
	if err != nil {
		log.Error("Error retrieving channel history:", err)
		return conversationHistory
	}

	totalTokens := 0
	maxHistoryTokens := maxTokens

	// Calculate total tokens without removing any messages
	for _, msg := range conversationHistory {
		totalTokens += len(msg.Content) + len(msg.Role) + 2
	}

	log.Info("Total Tokens Before Trimming:", totalTokens)

	// Iterate from the beginning of conversationHistory (oldest messages)
	for i := 0; i < len(conversationHistory); i++ {
		msg := conversationHistory[i]
		tokens := len(msg.Content) + len(msg.Role) + 2 // Account for role and content tokens

		if totalTokens-tokens >= maxHistoryTokens {
			// Remove the oldest message
			log.Info("Removing Oldest Message:", msg.Content)
			conversationHistory = conversationHistory[i+1:]
			i-- // Adjust index after removal
		} else {
			totalTokens -= tokens
		}
	}

	log.Info("Total Tokens After Trimming:", totalTokens)

	// Add new messages from the channel
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if len(message.Content) > 0 {
			tokens := len(message.Content) + 2 // Account for role and content tokens
			if totalTokens+tokens <= maxContextTokens {
				conversationHistory = append(conversationHistory, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: message.Content,
				})
				totalTokens += tokens
				log.Info("Adding New Message:", message.Content)
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

	// Log the final order of conversation history
	log.Info("Final Conversation History Order:", conversationHistory)

	return conversationHistory
}

func chatGPT(session *discordgo.Session, channelID string, message string, conversationHistory []openai.ChatCompletionMessage) {
	client := openai.NewClient(OpenAIToken)

	// Perform GPT-4 completion
	log.Info("Starting completion...", conversationHistory)
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			MaxTokens:        maxTokens,
			FrequencyPenalty: 0.3,
			PresencePenalty:  0.6,
			Model:            openai.GPT3Dot5Turbo,
			Messages:         conversationHistory, // Use trimmed conversation history
		},
	)
	log.Info("completion done.")

	// Handle API errors
	if err != nil {
		log.Error("Error connecting to the OpenAI API:", err)
		return
	}

	// Paginate the response and send as separate messages with clickable emojis
	gptResponse := resp.Choices[0].Message.Content
	pageSize := maxMessageTokens

	// Split the response into pages
	var pages []string
	for i := 0; i < len(gptResponse); i += pageSize {
		end := i + pageSize
		if end > len(gptResponse) {
			end = len(gptResponse)
		}
		pages = append(pages, gptResponse[i:end])
	}

	// Send the first page
	currentPage := 0
	totalPages := len(pages)
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("BitBot's Response (Page %d of %d)", currentPage+1, totalPages),
		Description: pages[currentPage],
		Color:       0x00ff00, // Green color
	}
	msg, err := session.ChannelMessageSendEmbed(channelID, embed)
	if err != nil {
		log.Error("Error sending embed message:", err)
		return
	}

	// Add reaction emojis for pagination if there are multiple pages
	if totalPages > 1 { // Only add reactions if there are multiple pages
		err = session.MessageReactionAdd(channelID, msg.ID, "⬅️")
		if err != nil {
			log.Error("Error adding reaction emoji:", err)
			return
		}
		err = session.MessageReactionAdd(channelID, msg.ID, "➡️")
		if err != nil {
			log.Error("Error adding reaction emoji:", err)
			return
		}
	}

	// Create a reaction handler function
	session.AddHandler(func(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
		// Call the reactionHandler function and pass totalPages
		reactionHandler(s, r, currentPage, msg, pages, totalPages)
	})

}

func reactionHandler(session *discordgo.Session, r *discordgo.MessageReactionAdd, currentPage int, msg *discordgo.Message, pages []string, totalPages int) {
	// Check if the reaction is from the same user and message
	if r.UserID == session.State.User.ID || r.MessageID != msg.ID {
		return
	}

	// Handle pagination based on reaction
	if r.Emoji.Name == "⬅️" {
		if currentPage > 0 {
			currentPage--
		}
	} else if r.Emoji.Name == "➡️" {
		if currentPage < len(pages)-1 {
			currentPage++
		}
	}

	// Update the message with the new page
	updatedEmbed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("BitBot's Response (Page %d of %d)", currentPage+1, len(pages)),
		Description: pages[currentPage],
		Color:       0x00ff00, // Green color
	}
	_, err := session.ChannelMessageEditEmbed(r.ChannelID, r.MessageID, updatedEmbed)
	if err != nil {
		log.Error("Error editing embed message:", err)
	}
}
