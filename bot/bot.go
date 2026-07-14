package bot

import (
	"bitbot/pb" // PocketBase interaction
	"context"
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
	RegoloAPIKey  string // Used by chat.go's InitRegoloClient
	RegoloModel   string // Optional; empty defaults to gpt-oss-120b
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
		{
			Name:        "mcp",
			Description: "Manage MCP tool servers (admin only).",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "add",
					Description: "Add and connect an MCP server.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Unique name for the server.", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "Streamable-HTTP MCP endpoint URL.", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "token", Description: "Optional bearer token.", Required: false},
					},
				},
				{
					Name:        "remove",
					Description: "Remove an MCP server and its tools.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Name of the server to remove.", Required: true},
					},
				},
				{Name: "list", Description: "List configured MCP servers and their status.", Type: discordgo.ApplicationCommandOptionSubCommand},
				{Name: "reload", Description: "Re-sync MCP servers from the database now.", Type: discordgo.ApplicationCommandOptionSubCommand},
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

	// Initialize services before opening Discord connection
	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("PocketBase initialized successfully.")

	if RegoloAPIKey == "" {
		log.Fatal("Regolo API Key (REGOLO_API_KEY) is not set in environment variables.")
	}
	log.Info("Initializing Regolo Client...")
	if err := InitRegoloClient(RegoloAPIKey, RegoloModel); err != nil {
		log.Fatalf("Failed to initialize Regolo Client: %v", err)
	}
	log.Info("Regolo Client initialized successfully.")

	// Register extended tools behind the toolbelt: SSH tools locally, plus any
	// tools exposed by a configured remote MCP server (non-fatal if unreachable).
	registerSSHTools()
	InitMCP(context.Background())

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

	log.Info("Registering commands...")
	registerCommands(discord, AppId)
	log.Info("BitBot is running...")

	go StartReminderScheduler(discord)

	log.Info("Exiting... press CTRL + c again")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

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

// CheckAdmin returns true if the user ID matches AllowedUserID or they have a matching role
func CheckAdmin(userID string, roles []string) bool {
	if userID == AllowedUserID {
		return true
	}
	return hasAdminRole(roles)
}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID || message.Content == "" {
		return
	}
	isPrivateChannel := message.GuildID == ""

	// After a restart, in-memory history is empty; seed it once from the channel's
	// prior messages so the bot still has earlier context. Anchored before the
	// current message so it isn't duplicated by the record below.
	getConversation(message.ChannelID).maybeBackfill(discord, message.ChannelID, message.ID, discord.State.User.ID)

	// Passive listening: record every human message (attributed to its speaker)
	// so the bot has full channel context and can answer "who said what" even for
	// messages that were not addressed to it.
	recordMessage(message.ChannelID, message.Author.ID, resolveDisplayName(message), message.Content)

	if strings.HasPrefix(message.Content, "!bit") || isPrivateChannel {
		chatbot(discord, message.Author.ID, message.ChannelID, message.GuildID)
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

// resolveDisplayName returns the best human-readable name for the message
// author: the per-guild nickname if set, otherwise the account display name,
// falling back to the username.
func resolveDisplayName(message *discordgo.MessageCreate) string {
	if message.Member != nil && message.Member.Nick != "" {
		return message.Member.Nick
	}
	if message.Author != nil && message.Author.GlobalName != "" {
		return message.Author.GlobalName
	}
	if message.Author != nil {
		return message.Author.Username
	}
	return "Unknown"
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
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				response, _ := GenerateSSHKeyCore(false)
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "showkey":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				response, _ := ShowSSHPublicKeyCore()
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "regenkey":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				response, _ := GenerateSSHKeyCore(true)
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "ssh":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				connectionDetails := data.Options[0].StringValue()
				response, _ := ConnectSSHServerCore(i.Member.User.ID, i.GuildID, connectionDetails)
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "exe":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				command := data.Options[0].StringValue()
				response, _ := ExecuteSSHCommandCore(i.Member.User.ID, i.GuildID, command)
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "exit":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				response, _ := CloseSSHConnectionCore(i.Member.User.ID, i.GuildID)
				respondWithMessage(s, i, response)
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "list":
			if CheckAdmin(i.Member.User.ID, i.Member.Roles) {
				response, _ := ListSSHServersCore(i.Member.User.ID, i.GuildID)
				respondWithMessage(s, i, response)
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
					"/list - List saved servers.\n" +
					"/mcp add|remove|list|reload - Manage MCP tool servers.\n"
			}
			respondWithMessage(s, i, helpMessage)

		case "remind":
			HandleRemindCommand(s, i)

		case "mcp":
			HandleMCPCommand(s, i)
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
