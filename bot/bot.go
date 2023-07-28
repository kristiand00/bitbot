package bot

import (
	"bitbot/pb"
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	openai "github.com/sashabaranov/go-openai"
)

var (
	CryptoToken string
	BotToken    string
	OpenAIToken string
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
	pb.Run()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

var conversationHistoryMap = make(map[string][]openai.ChatCompletionMessage)

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID {
		return
	}

	conversationHistory := conversationHistoryMap[message.Author.ID]

	userMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: message.Content,
	}
	conversationHistory = append(conversationHistory, userMessage)

	switch {
	case strings.Contains(message.Content, "!bit"):
		gptResponse := chatGPT(message.Content, conversationHistory)
		discord.ChannelTyping(message.ChannelID)
		discord.ChannelMessageSendComplex(message.ChannelID, gptResponse)

		botMessage := openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: gptResponse.Content,
		}
		conversationHistory = append(conversationHistory, botMessage)
	case strings.Contains(message.Content, "!cry"):
		currentCryptoPrice := getCurrentCryptoPrice(message.Content)
		discord.ChannelMessageSendComplex(message.ChannelID, currentCryptoPrice)
	}

	conversationHistoryMap[message.Author.ID] = conversationHistory
}
