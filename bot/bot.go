package bot

import (
	"bitbot/pb"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
)

var (
	BotToken      string
	OpenAIToken   string
	CryptoToken   string
	AllowedUserID string
	AppId 		  string
)

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal(err)
	}

	discord.AddHandler(newMessage)
	discord.AddHandler(commandHandler)

	log.Info("Opening Discord connection...")
	err = discord.Open()
    if err != nil {
        log.Fatal(err)
    }
	defer discord.Close()
	registerCommands(discord, AppId)
	log.Info("BitBot is running...")
	
	// Try initializing PocketBase after Discord is connected
	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("PocketBase initialized successfully.")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

var conversationHistoryMap = make(map[string][]map[string]interface{})
var sshConnections = make(map[string]*SSHConnection)

func hasAdminRole(roles []string) bool {
	for _, role := range roles {
		if role == AllowedUserID {
			return true
		}
	}
	return false
}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID || message.Content == "" {
		return
	}

	isPrivateChannel := message.GuildID == ""

	userID := message.Author.ID
	conversationHistory := conversationHistoryMap[userID]

	channelID := message.ChannelID
	conversationHistory = populateConversationHistory(discord, channelID, conversationHistory)

	if strings.HasPrefix(message.Content, "!cry") {
		currentCryptoPrice := getCurrentCryptoPrice(message.Content)
		discord.ChannelMessageSendComplex(message.ChannelID, currentCryptoPrice)
	} else if strings.HasPrefix(message.Content, "!bit") || isPrivateChannel {
		chatGPT(discord, message.ChannelID, conversationHistory)
	} else if strings.HasPrefix(message.Content, "!genkey") {
		if hasAdminRole(message.Member.Roles) {
			err := GenerateAndSaveSSHKeyPairIfNotExist()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error generating or saving key pair.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, "SSH key pair generated and saved successfully!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!showkey") {
		if hasAdminRole(message.Member.Roles) {
			publicKey, err := GetPublicKey()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error fetching public key.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, publicKey)
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!regenkey") {
		if hasAdminRole(message.Member.Roles) {
			err := GenerateAndSaveSSHKeyPair()
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error regenerating and saving key pair.")
				return
			}
			discord.ChannelMessageSend(message.ChannelID, "SSH key pair regenerated and saved successfully!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!ssh") {
		if hasAdminRole(message.Member.Roles) {
			commandParts := strings.Fields(message.Content)
			if len(commandParts) != 2 {
				discord.ChannelMessageSend(message.ChannelID, "Invalid command format. Use !ssh username@remote-host:port")
				return
			}

			connectionDetails := commandParts[1]
			sshConn, err := SSHConnectToRemoteServer(connectionDetails)
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error connecting to remote server.")
				return
			}

			// Store the SSH connection for later use
			sshConnections[message.Author.ID] = sshConn

			// Save server information to PocketBase
			serverInfo := &pb.ServerInfo{
				UserID:            message.Author.ID,
				ConnectionDetails: connectionDetails,
			}
			err = pb.CreateRecord("servers", serverInfo)
			if err != nil {
				log.Error(err)
				discord.ChannelMessageSend(message.ChannelID, "Error saving server information.")
				return
			}

			discord.ChannelMessageSend(message.ChannelID, "Connected to remote server!")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!exe") {
		if hasAdminRole(message.Member.Roles) {
			// Check if there is an active SSH connection for this user
			sshConn, ok := sshConnections[message.Author.ID]
			if !ok {
				discord.ChannelMessageSend(message.ChannelID, "You are not connected to any remote server. Use !ssh first.")
				return
			}

			// Extract the command after "!exe"
			command := strings.TrimPrefix(message.Content, "!exe ")

			// Execute the command on the remote server
			response, err := sshConn.ExecuteCommand(command)
			if err != nil {
				discord.ChannelMessageSend(message.ChannelID, "Error executing command on remote server.")
				return
			}

			discord.ChannelMessageSend(message.ChannelID, ">\n "+response)
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!exit") {
		if hasAdminRole(message.Member.Roles) {
			// Check if there is an active SSH connection for this user
			sshConn, ok := sshConnections[message.Author.ID]
			if !ok {
				discord.ChannelMessageSend(message.ChannelID, "You are not connected to any remote server. Use !ssh first.")
				return
			}

			// Close the SSH connection
			sshConn.Close()

			// Remove the SSH connection from the map
			delete(sshConnections, message.Author.ID)

			discord.ChannelMessageSend(message.ChannelID, "SSH connection closed.")
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!list") {
		if hasAdminRole(message.Member.Roles) {
			// Retrieve the list of servers for the user
			servers, err := pb.ListServersByUserID(message.Author.ID)
			if err != nil {
				log.Error("Error listing servers:", err)
				discord.ChannelMessageSend(message.ChannelID, "Error listing servers.")
				return
			}

			// Check if there are any servers
			if len(servers) == 0 {
				discord.ChannelMessageSend(message.ChannelID, "You don't have any servers.")
				return
			}

			// Build a message with the list of servers
			var serverListMessage strings.Builder
			serverListMessage.WriteString("Recent servers:\n")

			for _, server := range servers {
				serverListMessage.WriteString(fmt.Sprintf(server.ConnectionDetails))
				// Add other fields as needed
			}

			// Send the server list message to Discord
			discord.ChannelMessageSend(message.ChannelID, serverListMessage.String())
		} else {
			discord.ChannelMessageSend(message.ChannelID, "You are not authorized to use this command.")
		}
	} else if strings.HasPrefix(message.Content, "!help") {
		if strings.Contains(message.Content, "admin") {
			adminHelpMessage := "Admin commands:\n" +
				"!genkey - Generate and save SSH key pair.\n" +
				"!showkey - Show the public key.\n" +
				"!regenkey - Regenerate and save SSH key pair.\n" +
				"!ssh username@remote-host:port - Connect to a remote server via SSH.\n" +
				"!list saved servers. (auto save on first connect)\n" +
				"!exe command - Execute a command on the remote server (after !ssh).\n" +
				"!exit - Close the SSH connection (after !ssh).\n"

			discord.ChannelMessageSend(message.ChannelID, adminHelpMessage)
		} else {
			generalHelpMessage := "Available commands:\n" +
				"!cry - Get information about cryptocurrency prices.\n" +
				"!bit - Interact with the BitBot chatbot.\n" +
				"!roll - Random number from 1-100 or specify a number (!roll 9001).\n" +
				"!help - Show available commands.\n" +
				"!help admin - Show admin commands.\n"

			discord.ChannelMessageSend(message.ChannelID, generalHelpMessage)
		}
	} else if strings.HasPrefix(message.Content, "!roll") {
		handleRollCommand(discord, message)
	}

	conversationHistoryMap[userID] = conversationHistory
}
func handleRollCommand(session *discordgo.Session, message *discordgo.MessageCreate) {
	// Extract the argument from the message
	args := strings.Fields(message.Content[len("!roll"):])
	if len(args) == 0 {
		// No argument provided, generate random number between 1 and 100
		randomNumber := rand.Intn(100) + 1
		session.ChannelMessageSend(message.ChannelID, fmt.Sprintf("You rolled a %d!", randomNumber))
	} else {
		// Argument provided, try to parse it as a number
		if num, err := strconv.Atoi(args[0]); err == nil {
			// Generate random number between 1 and the provided number
			randomNumber := rand.Intn(num) + 1
			session.ChannelMessageSend(message.ChannelID, fmt.Sprintf("%d", randomNumber))
		} else {
			session.ChannelMessageSend(message.ChannelID, "Invalid argument. Please provide a valid number.")
		}
	}
}

func registerCommands(discord *discordgo.Session, appID string) {
    cmd := &discordgo.ApplicationCommand{
        Name:        "ping",
        Description: "Check the bot's ping.",
    }

    _, err := discord.ApplicationCommandCreate(appID, "", cmd)
    if err != nil {
        log.Fatalf("Cannot create slash command: %v", err)
    }
}

func commandHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
    if i.Type == discordgo.InteractionApplicationCommand {
        data := i.ApplicationCommandData()

        if data.Name == "ping" {
            err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
                Type: discordgo.InteractionResponseChannelMessageWithSource,
                Data: &discordgo.InteractionResponseData{
                    Content: "Pong!",
                },
            })
            if err != nil {
                log.Printf("Error responding to interaction: %v", err)
            }
        }
    }
}
