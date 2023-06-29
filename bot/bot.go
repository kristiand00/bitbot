package bot

import (
	"bitbot/pb"
	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"os"
	"os/signal"
	"strings"
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

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID {
		return
	}

	switch {
	case strings.Contains(message.Content, "!bit"):
		gptResponse := chatGPT(message.Content)
		discord.ChannelTyping(message.ChannelID)
		discord.ChannelMessageSendComplex(message.ChannelID, gptResponse)
	case strings.Contains(message.Content, "!cry"):
		currentCryptoPrice := getCurrentCryptoPrice(message.Content)
		discord.ChannelMessageSendComplex(message.ChannelID, currentCryptoPrice)
	}

}
