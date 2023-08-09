package bot

import (
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/sashabaranov/go-openai"
)

var (
	BotToken    string
	OpenAIToken string
	CryptoToken string
)

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal(err)
	}

	discord.AddHandler(newMessage)

	discord.Open()
	defer discord.Close()
	log.Info("BitBot is running...")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

var conversationHistoryMap = make(map[string][]openai.ChatCompletionMessage)

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID || message.Content == "" {
		return
	}

	isPrivateChannel := message.GuildID == ""

	userID := message.Author.ID
	conversationHistory := conversationHistoryMap[userID]

	channelID := message.ChannelID
	conversationHistory = populateConversationHistory(discord, channelID, conversationHistory)

	userMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message.Content,
	}

	conversationHistory = append(conversationHistory, userMessage)

	if strings.Contains(message.Content, "!bit") || isPrivateChannel {
		gptResponse := chatGPT(discord, message.ChannelID, message.Content, conversationHistory)
		discord.ChannelTyping(message.ChannelID)
		discord.ChannelMessageSendComplex(message.ChannelID, gptResponse)

		botMessage := openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: gptResponse.Content,
		}
		conversationHistory = append(conversationHistory, botMessage)
	} else if strings.Contains(message.Content, "!cry") {
		currentCryptoPrice := getCurrentCryptoPrice(message.Content)
		discord.ChannelMessageSendComplex(message.ChannelID, currentCryptoPrice)
	}

	conversationHistoryMap[userID] = conversationHistory
}
