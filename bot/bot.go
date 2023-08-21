package bot

import (
	"bitbot/pb"
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/sashabaranov/go-openai"
)

var (
	BotToken      string
	OpenAIToken   string
	CryptoToken   string
	AllowedUserID string
)

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal(err)
	}

	discord.AddHandler(newMessage)

	discord.Open()
	defer discord.Close()
	pb.Run()
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

	if strings.Contains(message.Content, "!cry") {
		currentCryptoPrice := getCurrentCryptoPrice(message.Content)
		discord.ChannelMessageSendComplex(message.ChannelID, currentCryptoPrice)
	} else if strings.Contains(message.Content, "!bit") || isPrivateChannel {
		chatGPT(discord, message.ChannelID, message.Content, conversationHistory)
	} else if strings.Contains(message.Content, "!genkey") {
		if message.Author.ID == AllowedUserID {
			err := GenerateAndSaveSSHKeyPairIfNotExist()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error generating or saving key pair.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, "SSH key pair generated and saved successfully!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.Contains(message.Content, "!showkey") {
		if message.Author.ID == AllowedUserID {
			publicKey, err := GetPublicKey()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error fetching public key.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, publicKey)
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.Contains(message.Content, "!regenkey") {
		if message.Author.ID == AllowedUserID {
			err := GenerateAndSaveSSHKeyPair()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error regenerating and saving key pair.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, "SSH key pair regenerated and saved successfully!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.Contains(message.Content, "!ssh") {
		if message.Author.ID == AllowedUserID {
			commandParts := strings.Fields(message.Content)
			if len(commandParts) != 2 {
				discord.ChannelMessageSend(message.ChannelID, "Invalid command format. Use !ssh username@remote-host:port")
				return
			}

			connectionDetails := commandParts[1]
			err := SSHConnectToRemoteServer(connectionDetails)
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error connecting to remote server.")
				return
			}

			discord.ChannelMessageSend(message.ChannelID, "Connected to remote server!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	}

	conversationHistoryMap[userID] = conversationHistory
}
