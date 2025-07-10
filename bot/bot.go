package bot

import (
	"bitbot/pb" // PocketBase interaction
	// For PCM to byte conversion
	"fmt"
	"math/rand"
	"os" // Restoring os import
	"os/signal"
	"strconv"
	"strings" // For RWMutex
	"time"    // Added for timeout in receiveOpusPackets

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	// No need to import reminder if it's in the same package; use local references instead.
)

var (
	// BotToken is injected at build time or via env
	BotToken      string
	GeminiAPIKey  string // This is already used by chat.go's InitGeminiClient
	CryptoToken   string
	AllowedUserID string
	AppId         string
)

// Command definitions
var (
	commands = []*discordgo.ApplicationCommand{
		{Name: "cry", Description: "Get information about cryptocurrency prices."},
		{Name: "genkey", Description: "Generate and save SSH key pair."},
		{Name: "showkey", Description: "Show the public SSH key."},
		{Name: "regenkey", Description: "Regenerate and save SSH key pair."},
		{Name: "createevent", Description: "Organize an Ava dungeon raid event."},
		{
			Name:        "ssh",
			Description: "Connect to a remote server via SSH.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "connection_details",
					Description: "Connection details in the format username@remote-host:port",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
		{
			Name:        "exe",
			Description: "Execute a command on the remote server.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "command",
					Description: "The command to execute.",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
		{Name: "exit", Description: "Close the SSH connection."},
		{Name: "list", Description: "List saved servers."},
		{
			Name:        "help",
			Description: "Show available commands.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "category",
					Description: "Specify 'admin' to view admin commands.",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
				},
			},
		},
		{
			Name:        "remind",
			Description: "Manage reminders.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "add",
					Description: "Add a new reminder.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "who",
							Description: "User(s) to remind (mention or ID, comma-separated). Use '@me' for yourself.",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "when",
							Description: "When to send the reminder (e.g., 'in 10m', 'tomorrow 10am', 'every Mon 9am').",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "message",
							Description: "The reminder message.",
							Required:    true,
						},
					},
				},
				{
					Name:        "list",
					Description: "List your active reminders.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				{
					Name:        "delete",
					Description: "Delete a reminder by its ID.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "id",
							Description: "The ID of the reminder to delete (from /remind list).",
							Required:    true,
						},
					},
				},
			},
		},
	}
	// registeredCommands is a map to keep track of registered commands and avoid re-registering.
	// This might be useful if registerCommands is called multiple times, though typically it's once at startup.
	// For now, we'll assume it's called once and simply iterate through `commands`.
	// var registeredCommands = make(map[string]*discordgo.ApplicationCommand)
)

var reminderLocation *time.Location

func init() {
	var err error
	reminderLocation, err = time.LoadLocation("Europe/Zagreb")
	if err != nil {
		reminderLocation = time.UTC
	}
}

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal(err)
	}

	discord.AddHandler(commandHandler)
	discord.AddHandler(newMessage)
	discord.AddHandler(modalHandler)
	discord.AddHandler(buttonHandler)

	log.Info("Opening Discord connection...")
	err = discord.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer discord.Close()

	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("PocketBase initialized successfully.")

	log.Info("Registering commands...")
	registerCommands(discord, AppId)
	log.Info("BitBot is running...")

	if GeminiAPIKey == "" {
		log.Fatal("Gemini API Key (GEMINI_API_KEY) is not set in environment variables.")
	}
	log.Info("Initializing Gemini Client...")
	if err := InitGeminiClient(GeminiAPIKey); err != nil {
		log.Fatalf("Failed to initialize Gemini Client: %v", err)
	}
	log.Info("Gemini Client initialized successfully.")

	go StartReminderScheduler(discord)

	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("Exiting... press CTRL + c again")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

var conversationHistoryMap = make(map[string][]map[string]interface{})

// sshConnections stores SSH connection details, keyed by "guildID:userID"
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

	if strings.HasPrefix(message.Content, "!bit") || isPrivateChannel {
		chatbot(discord, message.ChannelID, message.Content)
	}

	if strings.HasPrefix(message.Content, "!roll") {
		max := 100
		parts := strings.Fields(message.Content)
		if len(parts) > 1 {
			if val, err := strconv.Atoi(parts[1]); err == nil && val > 0 {
				max = val
			}
		}
		result := rand.Intn(max) + 1
		response := fmt.Sprintf("%s rolled: %d (1-%d)", message.Author.Mention(), result, max)
		discord.ChannelMessageSend(message.ChannelID, response)
		return
	}
}

func registerCommands(discord *discordgo.Session, appID string) {
	log.Infof("Registering %d commands.", len(commands))
	// To register for a specific guild, use:
	// discord.ApplicationCommandCreate(appID, "YOUR_GUILD_ID", cmd)
	for _, cmd := range commands {
		_, err := discord.ApplicationCommandCreate(appID, "", cmd) // Registering globally
		if err != nil {
			log.Fatalf("Cannot create slash command %q: %v", cmd.Name, err)
		}
		log.Infof("Successfully registered command: %s", cmd.Name)
	}
}

func commandHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionApplicationCommand {
		data := i.ApplicationCommandData()
		switch data.Name {
		case "createevent":
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseModal,
				Data: &discordgo.InteractionResponseData{
					CustomID: "event_modal",
					Title:    "Create an Ava Dungeon Raid",
					Components: []discordgo.MessageComponent{
						discordgo.ActionsRow{
							Components: []discordgo.MessageComponent{
								&discordgo.TextInput{
									CustomID:    "event_title",
									Label:       "Event Title",
									Style:       discordgo.TextInputShort,
									Placeholder: "Enter the raid title",
									Required:    true,
								},
							},
						},
						discordgo.ActionsRow{
							Components: []discordgo.MessageComponent{
								&discordgo.TextInput{
									CustomID:    "event_date",
									Label:       "Event Date",
									Style:       discordgo.TextInputShort,
									Placeholder: "e.g., 15-11-2024",
									Required:    true,
								},
							},
						},
						discordgo.ActionsRow{
							Components: []discordgo.MessageComponent{
								&discordgo.TextInput{
									CustomID:    "event_time",
									Label:       "Event Time",
									Style:       discordgo.TextInputShort,
									Placeholder: "e.g., 18:00 UTC",
									Required:    true,
								},
							},
						},
						discordgo.ActionsRow{
							Components: []discordgo.MessageComponent{
								&discordgo.TextInput{
									CustomID:    "event_note",
									Label:       "Additional Notes (optional)",
									Style:       discordgo.TextInputParagraph,
									Placeholder: "Any extra details or instructions",
									Required:    false,
								},
							},
						},
					},
				},
			})
			if err != nil {
				log.Printf("Error responding with modal: %v", err)
			}
		case "cry":
			currentCryptoPrice := getCurrentCryptoPrice(data.Options[0].StringValue())
			respondWithMessage(s, i, currentCryptoPrice)

		case "genkey":
			if hasAdminRole(i.Member.Roles) {
				err := GenerateAndSaveSSHKeyPairIfNotExist()
				response := "SSH key pair generated and saved successfully!"
				if err != nil {
					response = "Error generating or saving key pair."
				}
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "showkey":
			if hasAdminRole(i.Member.Roles) {
				publicKey, err := GetPublicKey()
				response := publicKey
				if err != nil {
					response = "Error fetching public key."
				}
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "regenkey":
			if hasAdminRole(i.Member.Roles) {
				err := GenerateAndSaveSSHKeyPair()
				response := "SSH key pair regenerated and saved successfully!"
				if err != nil {
					response = "Error regenerating and saving key pair."
				}
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "ssh":
			if hasAdminRole(i.Member.Roles) {
				connectionDetails := data.Options[0].StringValue()
				sshConn, err := SSHConnectToRemoteServer(connectionDetails)
				response := "Connected to remote server!"
				if err != nil {
					response = "Error connecting to remote server."
				} else {
					connectionKey := fmt.Sprintf("%s:%s", i.GuildID, i.Member.User.ID)
					sshConnections[connectionKey] = sshConn
					serverInfo := &pb.ServerInfo{UserID: i.Member.User.ID, GuildID: i.GuildID, ConnectionDetails: connectionDetails}
					err = pb.CreateRecord("servers", serverInfo)
					if err != nil {
						log.Error(err)
						response = "Error saving server information."
					}
				}
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "exe":
			if hasAdminRole(i.Member.Roles) {
				connectionKey := fmt.Sprintf("%s:%s", i.GuildID, i.Member.User.ID)
				sshConn, ok := sshConnections[connectionKey]
				if !ok {
					respondWithMessage(s, i, "You are not connected to any remote server in this guild. Use /ssh first.")
					return
				}
				command := data.Options[0].StringValue()
				response, err := sshConn.ExecuteCommand(command)
				if err != nil {
					response = "Error executing command on remote server."
				}
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "exit":
			if hasAdminRole(i.Member.Roles) {
				connectionKey := fmt.Sprintf("%s:%s", i.GuildID, i.Member.User.ID)
				sshConn, ok := sshConnections[connectionKey]
				if !ok {
					respondWithMessage(s, i, "You are not connected to any remote server in this guild. Use /ssh first.")
					return
				}
				sshConn.Close()
				delete(sshConnections, connectionKey)
				respondWithMessage(s, i, "SSH connection closed.")
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "list":
			if hasAdminRole(i.Member.Roles) {
				if i.GuildID == "" {
					respondWithMessage(s, i, "This command can only be used in a server.")
					return
				}
				servers, err := pb.ListServersByUserIDAndGuildID(i.Member.User.ID, i.GuildID)
				if err != nil {
					log.Errorf("Error listing servers for user %s in guild %s: %v", i.Member.User.ID, i.GuildID, err)
					respondWithMessage(s, i, "Could not retrieve server list. Please try again later.")
					return
				}
				if len(servers) == 0 {
					respondWithMessage(s, i, "You don't have any saved servers in this guild.")
					return
				}
				var serverListMessage strings.Builder
				serverListMessage.WriteString("Saved servers in this guild:\n")
				for _, server := range servers {
					serverListMessage.WriteString(fmt.Sprintf("- `%s`\n", server.ConnectionDetails))
				}
				respondWithMessage(s, i, serverListMessage.String())
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "help":
			helpMessage := "Available commands:\n" +
				"/cry - Get information about cryptocurrency prices.\n" +
				"/remind - Manage reminders.\n" +
				"    /remind add <who> <when> <message> - Add a reminder.\n" +
				"      <when> supports: 'in 10m', 'in 2h', 'in 3d', 'tomorrow at 8pm', 'next monday at 9:30am', 'every day at 8am', 'every monday 8pm', 'today at 8pm', 'at 8pm', '8pm', '20:00'\n" +
				"      Tips: If the time has already passed today, the reminder will be set for tomorrow.\n" +
				"      Use '@me' for yourself in <who>.\n" +
				"    /remind list - List your reminders.\n" +
				"    /remind delete <id> - Delete a reminder by its ID.\n" +
				"/help - Show available commands.\n"
			if len(data.Options) > 0 && data.Options[0].StringValue() == "admin" {
				helpMessage += "Admin commands:\n" +
					"/genkey - Generate and save SSH key pair.\n" +
					"/showkey - Show the public key.\n" +
					"/regenkey - Regenerate and save SSH key pair.\n" +
					"/ssh - Connect to a remote server via SSH.\n" +
					"/exe - Execute a command on the remote server.\n" +
					"/exit - Close the SSH connection.\n" +
					"/list - List saved servers.\n"
			}
			respondWithMessage(s, i, helpMessage)

		case "remind":
			HandleRemindCommand(s, i)
		}
	} else if i.Type == discordgo.InteractionModalSubmit {
		modalHandler(s, i)
	}
}

func modalHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionModalSubmit && i.ModalSubmitData().CustomID == "event_modal" {
		data := i.ModalSubmitData()

		title := data.Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
		date := data.Components[1].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
		time := data.Components[2].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
		note := data.Components[3].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value

		response := " \n"
		response += " **Ava Dungeon Raid Event Created!** \n"
		response += "**Title**: " + title + "\n"
		response += "**Date**: " + date + "\n"
		response += "**Time**: " + time + "\n"
		if note != "" {
			response += "**Note**: " + note
		}

		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: response,
				Components: []discordgo.MessageComponent{
					&discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							&discordgo.Button{
								Label:    "Coming",
								CustomID: "rsvp_coming",
								Style:    discordgo.PrimaryButton,
							},
							&discordgo.Button{
								Label:    "Benched",
								CustomID: "rsvp_bench",
								Style:    discordgo.SecondaryButton,
							},
							&discordgo.Button{
								Label:    "Not Coming",
								CustomID: "rsvp_not_coming",
								Style:    discordgo.DangerButton,
							},
						},
					},
				},
			},
		})
		if err != nil {
			log.Printf("Error responding to modal submission: %v", err)
		}
	}
}

// startReminderScheduler periodically checks for and dispatches due reminders.
func startReminderScheduler(s *discordgo.Session) {
	log.Info("Starting reminder scheduler...")
	// Check every minute. Adjust ticker duration as needed.
	// For testing, a shorter duration might be used, but 1 minute is reasonable for production.
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		log.Debug("Reminder scheduler ticked. Checking for due reminders...")
		processDueReminders(s)
	}
}

func respondWithMessage(s *discordgo.Session, i *discordgo.InteractionCreate, message interface{}) {
	var response *discordgo.InteractionResponseData

	switch v := message.(type) {
	case string:
		response = &discordgo.InteractionResponseData{
			Content: v,
			Flags:   discordgo.MessageFlagsEphemeral,
		}
	case *discordgo.MessageSend:
		response = &discordgo.InteractionResponseData{
			Content: v.Content,
			Embeds:  v.Embeds,
			Flags:   discordgo.MessageFlagsEphemeral,
		}
	default:
		response = &discordgo.InteractionResponseData{
			Content: "Unknown response type.",
			Flags:   discordgo.MessageFlagsEphemeral,
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: response,
	})
}

func buttonHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ButtonHandler(s, i)
}
