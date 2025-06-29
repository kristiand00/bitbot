package bot

import (
	"bitbot/pb"       // PocketBase interaction
	"context"         // For GenAI client
	"encoding/binary" // For PCM to byte conversion
	"errors"          // For error handling
	"fmt"
	"math/rand"
	"os" // Restoring os import
	"os/signal"
	"strings"
	"time" // Added for timeout in receiveOpusPackets

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"google.golang.org/api/option" // For GenAI client
	"io"                           // For io.EOF in GenAI receive
	"regexp"                       // For parsing time strings
	"strconv"                      // For parsing time strings
	"sync"                         // For RWMutex

	"github.com/pion/opus"    // Opus decoding (switched from layeh/gopus)
	"github.com/zaf/resample" // For resampling audio

	// "github.com/google/generative-ai-go/genai" // Old GenAI import
	"google.golang.org/genai" // New GenAI import
	// "google.golang.org/genai/types" // Removed as it's not a valid package in v1.10.0
)

var (
	// BotToken is injected at build time or via env
	BotToken      string
	GeminiAPIKey  string // This is already used by chat.go's InitGeminiClient
	CryptoToken   string
	AllowedUserID string
	AppId         string

	// ttsClient *texttospeech.Client // REMOVED
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
			Name:        "roll",
			Description: "Roll a random number.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "max",
					Description: "Specify the maximum number for the roll.",
					Type:        discordgo.ApplicationCommandOptionInteger,
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

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal(err)
	}

	discord.AddHandler(commandHandler)
	discord.AddHandler(newMessage)
	discord.AddHandler(modalHandler)

	log.Info("Opening Discord connection...")
	err = discord.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer discord.Close()
	log.Info("Registering commands...")
	registerCommands(discord, AppId)
	log.Info("BitBot is running...")

	// Initialize Gemini Client for chat functionalities
	if GeminiAPIKey == "" {
		log.Fatal("Gemini API Key (GEMINI_API_KEY) is not set in environment variables.")
	}
	log.Info("Initializing Gemini Client...")
	if err := InitGeminiClient(GeminiAPIKey); err != nil { // This function is in chat.go
		log.Fatalf("Failed to initialize Gemini Client: %v", err)
	}
	log.Info("Gemini Client initialized successfully.")

	userVoiceSessions = make(map[string]*UserVoiceSession)
	log.Info("User voice sessions map initialized.")

	// Start the reminder scheduler
	go startReminderScheduler(discord)

	// Initialize Google Cloud Text-to-Speech client - REMOVED
	// ctx := context.Background()
	// var errClient error
	// ttsClient, errClient = texttospeech.NewClient(ctx)
	// if errClient != nil {
	// 	log.Fatalf("Failed to create Google Cloud Text-to-Speech client: %v", errClient)
	// }
	// log.Info("Google Cloud Text-to-Speech client initialized successfully.")

	// Try initializing PocketBase after Discord is connected
	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("Exiting... press CTRL + c again")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

var conversationHistoryMap = make(map[string][]map[string]interface{})
var sshConnections = make(map[string]*SSHConnection)
var voiceConnections = make(map[string]*discordgo.VoiceConnection)

var userVoiceSessions map[string]*UserVoiceSession
var userVoiceSessionsMutex = sync.RWMutex{}

type UserVoiceSession struct {
	GenAISession      *genai.LiveSession // Changed from LiveSession to Session
	UserID            string
	GuildID           string
	DiscordSession    *discordgo.Session
	OriginalChannelID string
}

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

	if strings.HasPrefix(message.Content, "!joinvoice") {
		joinVoiceChannel(discord, message)
	} else if strings.HasPrefix(message.Content, "!leavevoice") {
		leaveVoiceChannel(discord, message)
	} else if strings.HasPrefix(message.Content, "!bit") || isPrivateChannel {
		chatGPT(discord, message.ChannelID, message.Content) // This function is in chat.go
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

func joinVoiceChannel(s *discordgo.Session, m *discordgo.MessageCreate) {
		}
	}
}

func joinVoiceChannel(s *discordgo.Session, m *discordgo.MessageCreate) {
	perms, err := s.UserChannelPermissions(s.State.User.ID, m.ChannelID)
	if err != nil {
		log.Errorf("Error getting user permissions: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Error checking permissions.")
		return
	}
	if perms&discordgo.PermissionVoiceConnect == 0 {
		s.ChannelMessageSend(m.ChannelID, "I don't have permission to connect to voice channels.")
		return
	}
	if perms&discordgo.PermissionVoiceSpeak == 0 {
		s.ChannelMessageSend(m.ChannelID, "I don't have permission to speak in voice channels.")
		return
	}

	guild, err := s.State.Guild(m.GuildID)
	if err != nil {
		log.Errorf("Error finding guild: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Error finding guild.")
		return
	}

	var voiceChannelID string
	for _, vs := range guild.VoiceStates {
		if vs.UserID == m.Author.ID {
			voiceChannelID = vs.ChannelID
			break
		}
	}

	if voiceChannelID == "" {
		s.ChannelMessageSend(m.ChannelID, "You are not in a voice channel.")
		return
	}

	vc, err := s.ChannelVoiceJoin(m.GuildID, voiceChannelID, false, false)
	if err != nil {
		log.Errorf("Error joining voice channel: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Error joining voice channel.")
		return
	}

	voiceConnections[m.GuildID] = vc
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Joined voice channel: %s", voiceChannelID))
	log.Infof("Joined voice channel: %s in guild: %s", voiceChannelID, m.GuildID)

	if vc != nil {
		log.Infof("Voice connection ready for guild %s. Launching Opus receiver.", m.GuildID)
		go receiveOpusPackets(vc, m.GuildID, m.ChannelID, s)
	} else {
		log.Errorf("Voice connection is nil after join for guild %s", m.GuildID)
	}
}

func receiveOpusPackets(vc *discordgo.VoiceConnection, guildID string, originalChannelID string, dgSession *discordgo.Session) {
	log.Infof("Starting Opus packet receiver for guild %s (original channel %s)", guildID, originalChannelID)
	defer log.Infof("Stopping Opus packet receiver for guild %s (original channel %s)", guildID, originalChannelID)

	if vc == nil || vc.OpusRecv == nil {
		log.Errorf("Voice connection or OpusRecv channel is nil for guild %s. Cannot receive packets.", guildID)
		return
	}

	decoder := opus.NewDecoder()
	log.Infof("pion/opus decoder created for guild %s", guildID)
	// The Decode method will return an error if it fails, which is handled later.

	const pcmFrameSize = 960
	pcmBuffer := make([]int16, pcmFrameSize)

	for {
		select {
		case packet, ok := <-vc.OpusRecv:
			var currentUserID string // Declare currentUserID
			// bytePcmBuffer for pion/opus Decode method which expects []byte
			bytePcmBuffer := make([]byte, pcmFrameSize*2) // *2 because pcmFrameSize is in int16 samples

			if !ok {
				log.Warnf("OpusRecv channel closed for guild %s. Exiting receiver goroutine.", guildID)
				userVoiceSessionsMutex.Lock()
				log.Warnf("OpusRecv channel closed for guild %s. Cleaning up associated UserVoiceSessions.", guildID)
				keysToDelete := []string{}
				for key, uvs := range userVoiceSessions {
					if uvs.GuildID == guildID {
						keysToDelete = append(keysToDelete, key)
					}
				}
				for _, key := range keysToDelete {
					uvs := userVoiceSessions[key]
					if uvs.GenAISession != nil {
						log.Infof("Closing GenAI session for user %s in guild %s due to OpusRecv channel closure.", uvs.UserID, guildID)
						uvs.GenAISession.Close()
					}
					delete(userVoiceSessions, key)
					log.Infof("Deleted UserVoiceSession for user %s in guild %s due to OpusRecv channel closure.", uvs.UserID, guildID)
				}
				userVoiceSessionsMutex.Unlock()
				return
			}

			if packet == nil || packet.Opus == nil {
				log.Debugf("Received nil packet or nil Opus data for guild %s, SSRC %d. Skipping.", guildID, packet.SSRC)
				continue
			}

			// Use bytePcmBuffer for decoding. Decode returns bandwidth, stereo status, and error.
			_, _, err := decoder.Decode(packet.Opus, bytePcmBuffer)
			if err != nil {
				log.Errorf("pion/opus failed to decode Opus packet for SSRC %d, guild %s: %v", packet.SSRC, guildID, err)
				continue
			}

			// Convert bytePcmBuffer back to pcmBuffer (int16)
			// n will be the number of int16 samples successfully read.
			var n int
			for i := 0; i < pcmFrameSize; i++ {
				if (i*2 + 1) < len(bytePcmBuffer) {
					pcmBuffer[i] = int16(binary.LittleEndian.Uint16(bytePcmBuffer[i*2 : i*2+2]))
					n = i + 1 // Count successfully converted samples
				} else {
					log.Warnf("Decoded byte buffer smaller than expected for pcmFrameSize. SSRC %d, guild %s. Read %d samples.", packet.SSRC, guildID, n)
					break
				}
			}
			if n == 0 && pcmFrameSize > 0 { // If nothing was decoded and we expected samples
				log.Warnf("No PCM samples decoded from Opus packet. SSRC %d, guild %s", packet.SSRC, guildID)
				continue
			}

			pcmDataForGenAI := make([]int16, n) // Use the actual number of decoded samples
			copy(pcmDataForGenAI, pcmBuffer[:n])

			// Unconditionally retrieve UserID by SSRC
			foundUser := ""
			// Note: The line below anticipates the fix from Step 4 (using dgSession instead of vc.Session)
			guildState, stateErr := dgSession.State.Guild(guildID)
			if stateErr == nil {
				for _, vs := range guildState.VoiceStates {
					if vs.SSRC == packet.SSRC { // packet.SSRC is valid
						foundUser = vs.UserID
						break
					}
				}
			}

			if foundUser == "" {
				log.Warnf("Could not find UserID for SSRC %d in guild %s. Skipping GenAI send.", packet.SSRC, guildID)
				continue // Skip processing this packet
			}
			currentUserID = foundUser // Assign to the new local variable

			userSession, err := establishAndManageVoiceSession(currentUserID, guildID, dgSession, originalChannelID)
			if err != nil || userSession == nil || userSession.GenAISession == nil {
				log.Errorf("Failed to establish or retrieve GenAI session for user %s in guild %s: %v. Skipping audio send.", currentUserID, guildID, err)
				continue
			}

			pcmBytes := make([]byte, len(pcmDataForGenAI)*2)
			for i, sVal := range pcmDataForGenAI {
				binary.LittleEndian.PutUint16(pcmBytes[i*2:], uint16(sVal))
			}

			mediaBlob := &genai.Blob{ // Reverted to genai.Blob
				MIMEType: "audio/l16;rate=48000;channels=1", // MIMEType remains correct
				Data:     pcmBytes,
			}
			// Assuming RealtimeInput is now a struct literal with an Audio field
			// and directly under genai.
			// realtimeInput := genai.LiveRealtimeInput{Audio: mediaBlob} // Changed to LiveRealtimeInput
			// errSend := userSession.GenAISession.SendRealtimeInput(realtimeInput) // Method name might change
			req := &genai.LiveRequest{
				Content: &genai.Content{
					Parts: []genai.Part{mediaBlob},
				},
			}
			errSend := userSession.GenAISession.Send(context.Background(), req)
			if errSend != nil {
				log.Errorf("receiveOpusPackets: SendRealtimeInput failed for user %s: %v", currentUserID, errSend)
			}

		case <-time.After(30 * time.Second):
			// log.Debugf("No Opus packet received for 30s in guild %s. Still listening...", guildID)
		}
	}
}

func establishAndManageVoiceSession(userID string, guildID string, dgSession *discordgo.Session, originalChannelID string) (*UserVoiceSession, error) {
	userSessionKey := guildID + ":" + userID
	userVoiceSessionsMutex.RLock()
	userSession, exists := userVoiceSessions[userSessionKey]
	userVoiceSessionsMutex.RUnlock()

	if !exists || userSession == nil || userSession.GenAISession == nil {
		log.Infof("No active GenAI Live session for user %s in guild %s. Establishing new session with AudioModelName.", userID, guildID)

		if geminiClient == nil {
			log.Error("GenAI client (geminiClient) is not initialized. Cannot establish voice session.")
			return nil, fmt.Errorf("geminiClient not initialized")
		}

		ctx := context.Background()
		modelName := AudioModelName

		// Types are now directly under 'genai' package.
		connectConfig := &genai.LiveConnectConfig{
			ResponseModalities: []genai.Modality{genai.ModalityAudio},
			SpeechConfig:       &genai.SpeechConfig{}, // Removed AudioEncoding and SampleRateHertz
			ContextWindowCompression: &genai.ContextWindowCompressionConfig{
				SlidingWindow: &genai.SlidingWindow{},
			},
		}
		log.Infof("Attempting to connect to GenAI Live with model: %s, output config: (using SDK defaults for speech)", modelName)

		// Use geminiClient.Live.Connect directly
		liveSession, err := geminiClient.Live.Connect(ctx, modelName, connectConfig)
		if err != nil {
			log.Errorf("Failed to connect to GenAI Live model '%s' for user %s, guild %s: %v", modelName, userID, guildID, err)
			return nil, err
		}
		log.Infof("GenAI Live session connected with model '%s' for user %s, guild %s.", modelName, userID, guildID)

		userVoiceSessionsMutex.Lock()
		userSession, exists = userVoiceSessions[userSessionKey]
		if !exists || userSession == nil {
			userSession = &UserVoiceSession{
				UserID:            userID,
				GuildID:           guildID,
				GenAISession:      liveSession,
				DiscordSession:    dgSession,
				OriginalChannelID: originalChannelID,
			}
			userVoiceSessions[userSessionKey] = userSession
			log.Infof("UserVoiceSession stored for user %s, guild %s.", userID, guildID)
		} else if userSession.GenAISession == nil {
			userSession.GenAISession = liveSession
			userSession.DiscordSession = dgSession
			userSession.OriginalChannelID = originalChannelID
			log.Infof("Updated existing UserVoiceSession with new GenAISession for user %s, guild %s.", userID, guildID)
		} else {
			log.Warnf("GenAI Live session for user %s guild %s already created by another goroutine. Closing redundant session.", userID, guildID)
			liveSession.Close()
		}
		userVoiceSessionsMutex.Unlock()

		if userSession.GenAISession == liveSession {
			go receiveAudioFromGenAI(userSession)
		}
	} else {
		log.Debugf("Reusing existing GenAI Live session for user %s in guild %s.", userID, guildID)
	}
	return userSession, nil
}

func sendAudioToDiscord(guildID string, userID string, pcmData []byte) {
	log.Infof("Preparing to send %d bytes of PCM audio to Discord for user %s in guild %s", len(pcmData), userID, guildID)

	userVoiceSessionsMutex.RLock()
	vc, vcExists := voiceConnections[guildID]
	userVoiceSessionsMutex.RUnlock()

	if !vcExists || vc == nil {
		log.Errorf("No active voice connection for guild %s to send audio.", guildID)
		return
	}

	if !vc.Ready {
		log.Errorf("Voice connection for guild %s is not ready. Cannot send audio.", guildID)
		return
	}

	encoder, err := opus.NewEncoder(48000, 1, opus.ApplicationVoIP)
	if err != nil {
		log.Errorf("Failed to create Opus encoder for guild %s: %v", guildID, err)
		return
	}

	if err := vc.Speaking(true); err != nil {
		log.Errorf("Failed to set speaking true for guild %s: %v", guildID, err)
	}
	defer vc.Speaking(false)

	const pcmFrameSamples = 960
	const pcmFrameBytes = pcmFrameSamples * 2
	opusBuf := make([]byte, 2048)
	pcmInt16Frame := make([]int16, pcmFrameSamples)
	totalSentBytes := 0

	for i := 0; i < len(pcmData); i += pcmFrameBytes {
		end := i + pcmFrameBytes
		var currentPcmFrameBytes []byte
		isLastFrame := false
		if end > len(pcmData) {
			currentPcmFrameBytes = pcmData[i:]
			isLastFrame = true
		} else {
			currentPcmFrameBytes = pcmData[i:end]
		}

		samplesInThisFrame := len(currentPcmFrameBytes) / 2
		for j := 0; j < samplesInThisFrame; j++ {
			if (j*2 + 1) < len(currentPcmFrameBytes) {
				pcmInt16Frame[j] = int16(binary.LittleEndian.Uint16(currentPcmFrameBytes[j*2:]))
			} else {
				pcmInt16Frame[j] = 0
			}
		}

		targetPcmSlice := pcmInt16Frame[:samplesInThisFrame]
		if isLastFrame && samplesInThisFrame < pcmFrameSamples && samplesInThisFrame > 0 {
			if samplesInThisFrame < pcmFrameSamples {
				for k := samplesInThisFrame; k < pcmFrameSamples; k++ {
					pcmInt16Frame[k] = 0
				}
				targetPcmSlice = pcmInt16Frame
			}
		} else if samplesInThisFrame == 0 {
			continue
		}

		n, err := encoder.Encode(targetPcmSlice, opusBuf)
		if err != nil {
			log.Errorf("Opus encoding failed for guild %s: %v", guildID, err)
			return
		}

		if n > 0 {
			select {
			case vc.OpusSend <- opusBuf[:n]:
				totalSentBytes += n
			case <-time.After(5 * time.Second):
				log.Errorf("Timeout sending Opus packet to guild %s. Sent %d bytes so far.", guildID, totalSentBytes)
				return
			}
		}
		time.Sleep(19 * time.Millisecond)
	}
	log.Infof("Finished sending audio to Discord for user %s in guild %s. Total Opus bytes sent: %d", userID, guildID, totalSentBytes)
}

func leaveVoiceChannel(s *discordgo.Session, m *discordgo.MessageCreate) {
	guildID := m.GuildID
	if guildID == "" {
		s.ChannelMessageSend(m.ChannelID, "This command can only be used in a server.")
		return
	}

	log.Infof("Received !leavevoice command for guild %s from user %s", guildID, m.Author.ID)

	userVoiceSessionsMutex.Lock()
	defer userVoiceSessionsMutex.Unlock()

	vc, vcExists := voiceConnections[guildID]
	if vcExists && vc != nil {
		log.Infof("Disconnecting from voice channel in guild %s", guildID)
		err := vc.Disconnect()
		if err != nil {
			log.Errorf("Error disconnecting voice connection for guild %s: %v", guildID, err)
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error leaving voice channel: %s", err.Error()))
		} else {
			s.ChannelMessageSend(m.ChannelID, "Left the voice channel.")
		}
		delete(voiceConnections, guildID)
		log.Infof("Removed voice connection entry for guild %s", guildID)
	} else {
		log.Info("Bot not in a voice channel in guild %s, but attempting cleanup of any stray GenAI sessions.", guildID)
		s.ChannelMessageSend(m.ChannelID, "I'm not currently in a voice channel in this server.")
	}

	keysToDelete := []string{}
	for key, uvs := range userVoiceSessions {
		if uvs.GuildID == guildID {
			keysToDelete = append(keysToDelete, key)
		}
	}

	for _, key := range keysToDelete {
		uvs := userVoiceSessions[key]
		if uvs.GenAISession != nil {
			log.Infof("Closing GenAI Live session for user %s in guild %s as part of leaving voice.", uvs.UserID, guildID)
			err := uvs.GenAISession.Close()
			if err != nil {
				log.Errorf("Error closing GenAI session for user %s in guild %s: %v", uvs.UserID, uvs.GuildID, err)
			}
		}
		delete(userVoiceSessions, key)
		log.Infof("Cleaned up UserVoiceSession for user %s in guild %s.", uvs.UserID, guildID)
	}
	if len(keysToDelete) > 0 {
		log.Infof("Cleaned up %d GenAI user voice sessions for guild %s.", len(keysToDelete), guildID)
	} else {
		log.Infof("No active GenAI user voice sessions found for guild %s to clean up.", guildID)
	}
}

func CleanupAllVoiceSessions() {
	log.Info("Cleaning up all voice sessions...")

	userVoiceSessionsMutex.Lock()
	defer userVoiceSessionsMutex.Unlock()

	if len(voiceConnections) > 0 {
		log.Infof("Found %d active Discord voice connections to cleanup.", len(voiceConnections))
		for guildID, vc := range voiceConnections {
			if vc != nil {
				log.Infof("Disconnecting voice connection for guild %s", guildID)
				if err := vc.Disconnect(); err != nil {
					log.Errorf("Error disconnecting voice connection for guild %s: %v", guildID, err)
				}
			}
		}
		voiceConnections = make(map[string]*discordgo.VoiceConnection)
		log.Info("Cleared all Discord voice connections.")
	} else {
		log.Info("No active Discord voice connections to cleanup.")
	}

	if len(userVoiceSessions) > 0 {
		log.Infof("Found %d active GenAI user voice sessions to cleanup.", len(userVoiceSessions))
		for _, uvs := range userVoiceSessions {
			if uvs != nil && uvs.GenAISession != nil {
				log.Infof("Closing GenAI Live session for user %s in guild %s", uvs.UserID, uvs.GuildID)
				uvs.GenAISession.Close()
			}
		}
		userVoiceSessions = make(map[string]*UserVoiceSession)
		log.Info("Cleared all GenAI user voice sessions.")
	} else {
		log.Info("No active GenAI user voice sessions to cleanup.")
	}
	log.Info("All voice sessions cleanup complete.")
}

func receiveAudioFromGenAI(userSession *UserVoiceSession) {
	if userSession == nil || userSession.GenAISession == nil {
		log.Error("Cannot receive audio from GenAI: user session or GenAISession is nil.")
		return
	}
	if userSession.DiscordSession == nil {
		log.Error("Discord session is nil in UserVoiceSession for user %s, guild %s. Cannot process text.", userSession.UserID, userSession.GuildID)
		userSession.GenAISession.Close()
		userVoiceSessionsMutex.Lock()
		delete(userVoiceSessions, userSession.GuildID+":"+userSession.UserID)
		userVoiceSessionsMutex.Unlock()
		return
	}

	userID := userSession.UserID
	guildID := userSession.GuildID
	userSessionKey := guildID + ":" + userID
	dgSession := userSession.DiscordSession
	originalChannelID := userSession.OriginalChannelID

	log.Infof("Starting GenAI audio receiver goroutine for user %s in guild %s (original channel %s).", userID, guildID, originalChannelID)

	defer func() {
		log.Infof("Stopping GenAI audio receiver for user %s in guild %s (original channel %s).", userID, guildID, originalChannelID)
		userVoiceSessionsMutex.Lock()
		currentSessionInMap, ok := userVoiceSessions[userSessionKey]
		if ok && currentSessionInMap == userSession {
			delete(userVoiceSessions, userSessionKey)
		}
		userVoiceSessionsMutex.Unlock()
		userSession.GenAISession.Close()
		log.Infof("GenAI session fully closed for user %s, guild %s (audio receiver).", userID, guildID)
	}()

	for {
		msg, err := userSession.GenAISession.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "stream closed") || strings.Contains(err.Error(), "session closed") {
				log.Infof("GenAI session stream closed/ended for user %s, guild %s (audio receiver): %v", userID, guildID, err)
			} else {
				log.Errorf("Error receiving audio from GenAI for user %s, guild %s: %v", userID, guildID, err)
			}
			return // Exit goroutine
		}

		modelRespondedWithAudio := false
		if msg != nil {
			// Access ModelTurn directly and check if ServerContent itself is nil first
			if msg.ServerContent != nil && msg.ServerContent.ModelTurn != nil {
				modelTurn := msg.ServerContent.ModelTurn // Changed GetModelTurn() to direct field access
				for _, part := range modelTurn.Parts {
					// Check for InlineData for audio, and Text for potential text parts
					if part.InlineData != nil && strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
						audioBytes := part.InlineData.Data
						if len(audioBytes) > 0 {
							log.Infof("Received audio data blob from GenAI for user %s. MIME: %s, Size: %d bytes.", userID, part.InlineData.MIMEType, len(audioBytes))
							go processAndSendDiscordAudioResponse(dgSession, guildID, userID, audioBytes, 24000) // Assuming 24000Hz output
							modelRespondedWithAudio = true
							break // from inner loop over parts
						}
					} else if textVal := part.Text; textVal != "" { // Using direct field access for Text
						// Handle potential text parts if mixed modality is possible or for debugging
						log.Debugf("Received text part from GenAI audio stream for user %s: %s", userID, textVal)
					}
				}
				if !modelRespondedWithAudio {
					log.Warnf("GenAI ModelTurn for user %s (audio expected) had parts, but no audio/media part with InlineData found.", userID)
				}
				// Removed the msg.Error block, error is handled from Receive() call directly.
				// The GenAI library typically surfaces errors through the 'err' return of Receive().
				// If specific error codes/messages were previously on msg.Error, they might be in err now, or logged by the SDK.
			} else if msg.ServerContent == nil { // If there's no ServerContent and no error from Receive(), it's unusual.
				log.Warnf("Received GenAI message for user %s (audio expected) with no ServerContent and no error from Receive(). Msg: %+v", userID, msg)
			}
			// If ServerContent is not nil, but ModelTurn is nil, it might be another type of message (e.g. interim results if enabled)
			// For now, we are only interested in ModelTurn for audio.
		}
	}
}

func processAndSendDiscordAudioResponse(dgSession *discordgo.Session, guildID string, userID string, genaiAudioData []byte, inputSampleRateHz int) {
	log.Infof("Processing %d bytes of GenAI audio data at %dHz for user %s, guild %s.", len(genaiAudioData), inputSampleRateHz, userID, guildID)

	userVoiceSessionsMutex.RLock()
	vc, vcExists := voiceConnections[guildID]
	originalChannelID := ""
	if uvs, uvsExists := userVoiceSessions[guildID+":"+userID]; uvsExists {
		originalChannelID = uvs.OriginalChannelID
	}
	userVoiceSessionsMutex.RUnlock()

	if !vcExists || vc == nil || !vc.Ready {
		log.Errorf("No active/ready voice connection for guild %s to send audio response for user %s.", guildID, userID)
		if originalChannelID != "" {
			dgSession.ChannelMessageSend(originalChannelID, fmt.Sprintf("Sorry <@%s>, I couldn't send the voice response as I'm not properly connected to voice.", userID))
		}
		return
	}

	if len(genaiAudioData)%2 != 0 {
		log.Errorf("GenAI audio data length is not even for user %s, guild %s. Size: %d", userID, guildID, len(genaiAudioData))
		return
	}
	pcmInt16Input := make([]int16, len(genaiAudioData)/2)
	for i := 0; i < len(pcmInt16Input); i++ {
		pcmInt16Input[i] = int16(binary.LittleEndian.Uint16(genaiAudioData[i*2:]))
	}

	pcmFloat64Input := make([]float64, len(pcmInt16Input))
	for i, s := range pcmInt16Input {
		pcmFloat64Input[i] = float64(s) / 32768.0
	}

	discordTargetSampleRate := 48000
	var pcmFloat64AtTargetRate []float64

	if inputSampleRateHz != discordTargetSampleRate {
		log.Infof("Resampling audio for user %s from %dHz to %dHz.", userID, inputSampleRateHz, discordTargetSampleRate)
		resampled, err := resample.Resample(inputSampleRateHz, discordTargetSampleRate, 1, pcmFloat64Input)
		if err != nil {
			log.Errorf("Failed to resample audio for user %s: %v", userID, err)
			if originalChannelID != "" {
				dgSession.ChannelMessageSend(originalChannelID, fmt.Sprintf("Sorry <@%s>, I had trouble processing the voice response (resampling failed).", userID))
			}
			return
		}
		pcmFloat64AtTargetRate = resampled
		log.Infof("Resampling complete for user %s. Original samples: %d, Resampled samples: %d", userID, len(pcmFloat64Input), len(pcmFloat64AtTargetRate))
	} else {
		log.Infof("Audio for user %s is already at target sample rate %dHz.", userID, discordTargetSampleRate)
		pcmFloat64AtTargetRate = pcmFloat64Input
	}

	pcmInt16Output := make([]int16, len(pcmFloat64AtTargetRate))
	for i, s := range pcmFloat64AtTargetRate {
		val := s * 32767.0
		if val > 32767.0 {
			val = 32767.0
		}
		if val < -32768.0 {
			val = -32768.0
		}
		pcmInt16Output[i] = int16(val)
	}

	encoder, err := opus.NewEncoder(discordTargetSampleRate, 1, opus.ApplicationVoIP)
	if err != nil {
		log.Errorf("Failed to create Opus encoder for TTS response (user %s, guild %s): %v", userID, guildID, err)
		return
	}

	if err := vc.Speaking(true); err != nil {
		log.Errorf("Failed to set speaking true for TTS response (user %s, guild %s): %v", userID, guildID, err)
	}
	defer vc.Speaking(false)

	const pcmFrameSamples = 960
	opusBuf := make([]byte, 2048)
	totalOpusBytesSent := 0

	for i := 0; i < len(pcmInt16Output); i += pcmFrameSamples {
		end := i + pcmFrameSamples
		var pcmFrameCurrent []int16
		if end > len(pcmInt16Output) {
			pcmFrameCurrent = make([]int16, pcmFrameSamples)
			copy(pcmFrameCurrent, pcmInt16Output[i:])
		} else {
			pcmFrameCurrent = pcmInt16Output[i:end]
		}

		n, err := encoder.Encode(pcmFrameCurrent, opusBuf)
		if err != nil {
			log.Errorf("Opus encoding failed for TTS response (user %s, guild %s): %v", userID, guildID, err)
			return
		}

		if n > 0 {
			select {
			case vc.OpusSend <- opusBuf[:n]:
				totalOpusBytesSent += n
			case <-time.After(5 * time.Second):
				log.Errorf("Timeout sending Opus packet for TTS response (user %s, guild %s). Sent %d bytes.", userID, guildID, totalOpusBytesSent)
				return
			}
		}
		time.Sleep(19 * time.Millisecond)
	}
	log.Infof("Finished sending TTS audio response to Discord for user %s in guild %s. Total Opus bytes: %d", userID, guildID, totalOpusBytesSent)
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
					sshConnections[i.Member.User.ID] = sshConn
					serverInfo := &pb.ServerInfo{UserID: i.Member.User.ID, ConnectionDetails: connectionDetails}
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
				sshConn, ok := sshConnections[i.Member.User.ID]
				if !ok {
					respondWithMessage(s, i, "You are not connected to any remote server. Use /ssh first.")
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
				sshConn, ok := sshConnections[i.Member.User.ID]
				if !ok {
					respondWithMessage(s, i, "You are not connected to any remote server. Use /ssh first.")
					return
				}
				sshConn.Close()
				delete(sshConnections, i.Member.User.ID)
				respondWithMessage(s, i, "SSH connection closed.")
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "list":
			if hasAdminRole(i.Member.Roles) {
				servers, err := pb.ListServersByUserID(i.Member.User.ID)
				if err != nil || len(servers) == 0 {
					respondWithMessage(s, i, "You don't have any servers.")
					return
				}
				var serverListMessage strings.Builder
				serverListMessage.WriteString("Recent servers:\n")
				for _, server := range servers {
					serverListMessage.WriteString(fmt.Sprintf("%s\n", server.ConnectionDetails))
				}
				respondWithMessage(s, i, serverListMessage.String())
			} else {
				respondWithMessage(s, i, "You are not authorized to use this command.")
			}

		case "help":
			helpMessage := "Available commands:\n" +
				"/cry - Get information about cryptocurrency prices.\n" +
				"/roll - Roll a random number.\n" +
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

		case "roll":
			max := 100
			if len(data.Options) > 0 {
				max = int(data.Options[0].IntValue())
			}
			result := rand.Intn(max) + 1
			respondWithMessage(s, i, fmt.Sprintf("You rolled: %d", result))
		case "remind":
			// Delegate to a sub-handler for /remind subcommands
			handleRemindCommand(s, i)
		}
	} else if i.Type == discordgo.InteractionModalSubmit {
		modalHandler(s, i)
	}
}

// handleRemindCommand delegates processing for /remind subcommands
func handleRemindCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	subCommand := i.ApplicationCommandData().Options[0].Name
	switch subCommand {
	case "add":
		handleAddReminder(s, i)
	case "list":
		handleListReminders(s, i)
	case "delete":
		handleDeleteReminder(s, i)
	default:
		respondWithMessage(s, i, "Unknown remind subcommand.")
	}
}

// handleAddReminder processes the /remind add command.
func handleAddReminder(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options[0].Options // Options for the "add" subcommand

	var whoArg, whenArg, messageArg string
	for _, opt := range options {
		switch opt.Name {
		case "who":
			whoArg = opt.StringValue()
		case "when":
			whenArg = opt.StringValue()
		case "message":
			messageArg = opt.StringValue()
		}
	}

	if whoArg == "" || whenArg == "" || messageArg == "" {
		respondWithMessage(s, i, "Missing required arguments for adding a reminder.")
		return
	}

	// 1. Parse 'who' argument
	var targetUserIDs []string
	rawTargetUserIDs := strings.Split(whoArg, ",")
	for _, idStr := range rawTargetUserIDs {
		trimmedID := strings.TrimSpace(idStr)
		if trimmedID == "@me" {
			targetUserIDs = append(targetUserIDs, i.Member.User.ID)
		} else {
			// Basic validation: check if it's a user mention or a raw ID
			// <@USER_ID> or <@!USER_ID>
			re := regexp.MustCompile(`<@!?(\d+)>`)
			matches := re.FindStringSubmatch(trimmedID)
			if len(matches) == 2 {
				targetUserIDs = append(targetUserIDs, matches[1])
			} else if _, err := strconv.ParseUint(trimmedID, 10, 64); err == nil {
				// Looks like a raw ID
				targetUserIDs = append(targetUserIDs, trimmedID)
			} else {
				respondWithMessage(s, i, fmt.Sprintf("Invalid user format: '%s'. Please use @mention, user ID, or '@me'.", trimmedID))
				return
			}
		}
	}
	if len(targetUserIDs) == 0 {
		respondWithMessage(s, i, "No valid target users specified.")
		return
	}
	// Remove duplicates
	seen := make(map[string]bool)
	uniqueTargetUserIDs := []string{}
	for _, id := range targetUserIDs {
		if !seen[id] {
			seen[id] = true
			uniqueTargetUserIDs = append(uniqueTargetUserIDs, id)
		}
	}
	targetUserIDs = uniqueTargetUserIDs


	// 2. Parse 'when' argument (initial simple parsing)
	// TODO: Expand this with more robust parsing (date, time, recurring)
	reminderTime, isRecurring, recurrenceRule, err := parseWhenSimple(whenArg)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("Error parsing 'when' argument: %v. Supported formats: 'in Xm/Xh/Xd'", err))
		return
	}

	// 3. Create Reminder struct
	reminder := &pb.Reminder{
		UserID:         i.Member.User.ID,
		TargetUserIDs:  targetUserIDs,
		Message:        messageArg,
		ChannelID:      i.ChannelID,
		GuildID:        i.GuildID, // Will be empty for DMs, which is fine
		ReminderTime:   reminderTime,
		IsRecurring:    isRecurring,
		RecurrenceRule: recurrenceRule,
	}
	// For non-recurring, NextReminderTime is same as ReminderTime initially by GetDueReminders logic.
	// For recurring, NextReminderTime should be the first actual occurrence.
	// Our simple parser sets ReminderTime to the first occurrence.
	// If it's recurring, we'll set NextReminderTime to this first occurrence.
	if isRecurring {
		reminder.NextReminderTime = reminderTime
	}


	// 4. Save to PocketBase
	err = pb.CreateReminder(reminder)
	if err != nil {
		log.Errorf("Failed to create reminder: %v", err)
		respondWithMessage(s, i, "Sorry, I couldn't save your reminder. Please try again later.")
		return
	}

	// 5. Confirm to user
	var targetUsersString []string
	for _, uid := range targetUserIDs {
		targetUsersString = append(targetUsersString, fmt.Sprintf("<@%s>", uid))
	}

	timeFormat := "Jan 2, 2006 at 3:04 PM MST"
	confirmationMsg := fmt.Sprintf("Okay, I'll remind %s on %s about: \"%s\"",
		strings.Join(targetUsersString, ", "),
		reminderTime.Local().Format(timeFormat), // Display in local time for confirmation
		messageArg)
	if isRecurring {
		confirmationMsg += fmt.Sprintf(" (recurs %s)", recurrenceRule)
	}

	respondWithMessage(s, i, confirmationMsg)
}

// parseWhenSimple is a basic parser for "in Xm/h/d" and "every Xm/h/d" type strings.
// Returns: reminderTime, isRecurring, recurrenceRule, error
func parseWhenSimple(whenStr string) (time.Time, bool, string, error) {
	whenStr = strings.ToLower(strings.TrimSpace(whenStr))
	now := time.Now()
	isRecurring := false
	recurrenceRule := ""

	// Check for "every" keyword for recurrence
	if strings.HasPrefix(whenStr, "every ") {
		isRecurring = true
		whenStr = strings.TrimPrefix(whenStr, "every ")
		// For simple "every Xunit", the rule is just the unit for now.
		// More complex parsing will set a better rule.
	}

	// Regex for "in Xunit" or "Xunit"
	// Example: "in 10m", "10m", "in 2h", "2h", "in 3d", "3d"
	re := regexp.MustCompile(`^(?:in\s+)?(\d+)\s*([mhd])$`)
	matches := re.FindStringSubmatch(whenStr)

	if len(matches) == 3 {
		value, err := strconv.Atoi(matches[1])
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid number: %s", matches[1])
		}
		unit := matches[2]
		duration := time.Duration(value)

		switch unit {
		case "m":
			duration *= time.Minute
			if isRecurring { recurrenceRule = fmt.Sprintf("every %d minutes", value) }
		case "h":
			duration *= time.Hour
			if isRecurring { recurrenceRule = fmt.Sprintf("every %d hours", value) }
		case "d":
			duration *= time.Hour * 24
			if isRecurring { recurrenceRule = fmt.Sprintf("every %d days", value) }
		default:
			return time.Time{}, false, "", fmt.Errorf("unknown time unit: %s", unit)
		}

		if isRecurring && recurrenceRule == "" { // Should be set by above cases
			return time.Time{}, false, "", fmt.Errorf("could not determine recurrence rule for: %s", whenStr)
		}

		return now.Add(duration), isRecurring, recurrenceRule, nil
	}

	// TODO: Add more parsing logic here (e.g., "tomorrow at 10am", "next Monday at 3pm", "every day at 9am")
	// For now, only "in Xm/h/d" is supported for non-recurring.
	// And "every Xm/h/d" for recurring.
	if isRecurring {
		return time.Time{}, false, "", fmt.Errorf("unsupported recurring format: '%s'. Try 'every Xm/Xh/Xd'", whenStr)
	}
	return time.Time{}, false, "", fmt.Errorf("unsupported time format: '%s'. Try 'in Xm/Xh/Xd'", whenStr)
}


func handleListReminders(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := i.Member.User.ID
	reminders, err := pb.ListRemindersByUser(userID)
	if err != nil {
		log.Errorf("Failed to list reminders for user %s: %v", userID, err)
		respondWithMessage(s, i, "Could not fetch your reminders. Please try again later.")
		return
	}

	if len(reminders) == 0 {
		respondWithMessage(s, i, "You have no active reminders.")
		return
	}

	var response strings.Builder
	response.WriteString("**Your active reminders:**\n")
	timeFormat := "Jan 2, 2006 at 3:04 PM MST"

	for idx, r := range reminders {
		var nextDue time.Time
		if r.IsRecurring {
			nextDue = r.NextReminderTime
		} else {
			nextDue = r.ReminderTime
		}

		// Ensure we have a valid time to format
		var nextDueStr string
		if !nextDue.IsZero() {
			nextDueStr = nextDue.Local().Format(timeFormat)
		} else {
			nextDueStr = "N/A (Error in time)" // Should ideally not happen with current logic
		}

		var targets []string
		for _, tUID := range r.TargetUserIDs {
			if tUID == userID {
				targets = append(targets, "@me")
			} else {
				targets = append(targets, fmt.Sprintf("<@%s>", tUID))
			}
		}
		targetStr := strings.Join(targets, ", ")

		response.WriteString(fmt.Sprintf("%d. **ID**: `%s`\n", idx+1, r.ID))
		response.WriteString(fmt.Sprintf("   **To**: %s\n", targetStr))
		response.WriteString(fmt.Sprintf("   **Message**: %s\n", r.Message))
		response.WriteString(fmt.Sprintf("   **Next Due**: %s\n", nextDueStr))
		if r.IsRecurring {
			response.WriteString(fmt.Sprintf("   **Recurs**: %s\n", r.RecurrenceRule))
		}
		response.WriteString("\n")
	}

	// Discord messages have a length limit (usually 2000 characters).
	// If the list is too long, we might need to paginate or send in multiple messages.
	// For now, send as one, assuming it won't be excessively long for typical use.
	if response.Len() > 1900 { // Leave some buffer
		respondWithMessage(s, i, "You have too many reminders to display in one message. Please delete some old ones if possible. (Full list display for very long lists is a TODO)")
		return
	}

	respondWithMessage(s, i, response.String())
}

func handleDeleteReminder(s *discordgo.Session, i *discordgo.InteractionCreate) {
	deleterUserID := i.Member.User.ID
	reminderIDToDelete := i.ApplicationCommandData().Options[0].Options[0].StringValue() // Subcommand "delete" -> option "id"

	if reminderIDToDelete == "" {
		respondWithMessage(s, i, "You must provide a reminder ID to delete.")
		return
	}

	reminder, err := pb.GetReminderByID(reminderIDToDelete)
	if err != nil {
		// Check if it's a "not found" error
		if strings.Contains(err.Error(), "Failed to find record") || strings.Contains(err.Error(), "not found") { // Adjust based on PocketBase error messages
			log.Warnf("User %s tried to delete non-existent reminder ID %s: %v", deleterUserID, reminderIDToDelete, err)
			respondWithMessage(s, i, fmt.Sprintf("Could not find a reminder with ID: `%s`.", reminderIDToDelete))
			return
		}
		log.Errorf("Error fetching reminder ID %s for deletion by user %s: %v", reminderIDToDelete, deleterUserID, err)
		respondWithMessage(s, i, "Could not fetch the reminder for deletion. Please try again.")
		return
	}

	// Authorization: Check if the user trying to delete is the one who created it.
	// TODO: Add admin override if desired (e.g., check for admin role)
	if reminder.UserID != deleterUserID {
		log.Warnf("User %s attempted to delete reminder ID %s owned by user %s.", deleterUserID, reminderIDToDelete, reminder.UserID)
		respondWithMessage(s, i, "You can only delete reminders that you created.")
		return
	}

	err = pb.DeleteReminder(reminderIDToDelete)
	if err != nil {
		log.Errorf("Failed to delete reminder ID %s for user %s: %v", reminderIDToDelete, deleterUserID, err)
		respondWithMessage(s, i, fmt.Sprintf("Failed to delete reminder `%s`. Please try again.", reminderIDToDelete))
		return
	}

	log.Infof("User %s successfully deleted reminder ID %s.", deleterUserID, reminderIDToDelete)
	respondWithMessage(s, i, fmt.Sprintf("Successfully deleted reminder with ID: `%s`.", reminderIDToDelete))
}

// startReminderScheduler periodically checks for and dispatches due reminders.
func startReminderScheduler(s *discordgo.Session) {
	log.Info("Starting reminder scheduler...")
	// Check every minute. Adjust ticker duration as needed.
	// For testing, a shorter duration might be used, but 1 minute is reasonable for production.
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Debug("Reminder scheduler ticked. Checking for due reminders...")
			processDueReminders(s)
		}
	}
}

// processDueReminders fetches and handles due reminders.
func processDueReminders(s *discordgo.Session) {
	dueReminders, err := pb.GetDueReminders()
	if err != nil {
		log.Errorf("Error fetching due reminders: %v", err)
		return
	}

	if len(dueReminders) == 0 {
		log.Debug("No due reminders found.")
		return
	}

	log.Infof("Found %d due reminder(s). Processing...", len(dueReminders))

	for _, reminder := range dueReminders {
		var mentions []string
		for _, userID := range reminder.TargetUserIDs {
			mentions = append(mentions, fmt.Sprintf("<@%s>", userID))
		}
		fullMessage := fmt.Sprintf("%s Hey! Here's your reminder: %s", strings.Join(mentions, " "), reminder.Message)

		_, err := s.ChannelMessageSend(reminder.ChannelID, fullMessage)
		if err != nil {
			log.Errorf("Failed to send reminder message for reminder ID %s to channel %s: %v", reminder.ID, reminder.ChannelID, err)
			// Decide if we should retry or skip. For now, skip.
			// If the channel or bot permissions are an issue, retrying might not help.
			continue
		}
		log.Infof("Sent reminder ID %s to channel %s for users %v.", reminder.ID, reminder.ChannelID, reminder.TargetUserIDs)

		if !reminder.IsRecurring {
			err := pb.DeleteReminder(reminder.ID)
			if err != nil {
				log.Errorf("Failed to delete non-recurring reminder ID %s: %v", reminder.ID, err)
			} else {
				log.Infof("Deleted non-recurring reminder ID %s.", reminder.ID)
			}
		} else {
			// Handle recurring reminder: calculate next time and update
			nextTime, errCalc := calculateNextRecurrence(reminder.ReminderTime, reminder.RecurrenceRule, reminder.LastTriggeredAt)
			if errCalc != nil {
				log.Errorf("Failed to calculate next recurrence for reminder ID %s: %v. Deleting reminder to prevent loop.", reminder.ID, errCalc)
				// If calculation fails, delete it to avoid it getting stuck.
				pb.DeleteReminder(reminder.ID)
				continue
			}

			reminder.NextReminderTime = nextTime
			reminder.LastTriggeredAt = time.Now().UTC() // Set last triggered to now

			errUpdate := pb.UpdateReminder(reminder)
			if errUpdate != nil {
				log.Errorf("Failed to update recurring reminder ID %s with next time %v: %v", reminder.ID, nextTime, errUpdate)
			} else {
				log.Infof("Updated recurring reminder ID %s. Next occurrence: %s", reminder.ID, nextTime.Format(time.RFC1123))
			}
		}
	}
}

// calculateNextRecurrence calculates the next time for a recurring reminder.
// This is a simplified version based on parseWhenSimple's output.
// TODO: Enhance this to parse more complex RecurrenceRule (e.g., cron strings, specific days).
func calculateNextRecurrence(originalReminderTime time.Time, rule string, lastTriggeredTime time.Time) (time.Time, error) {
	now := time.Now()
	// If lastTriggeredTime is zero (first time for a recurring one after initial setup),
	// or if originalReminderTime is in the future (e.g. reminder set "every day at 9am" but it's 8am now),
	// the next time *might* still be the originalReminderTime if it hasn't passed.
	// However, GetDueReminders should only return it if originalReminderTime/NextReminderTime is past.
	// So, we assume lastTriggeredTime is the correct base if available and non-zero.

	baseTime := lastTriggeredTime
	if baseTime.IsZero() { // If it was never triggered (e.g. just created recurring)
		baseTime = originalReminderTime // This was the first scheduled time.
	}

	// Ensure baseTime is not in the future relative to now, as we're calculating the *next* one from *now* or *last trigger*.
    // If the calculated baseTime (original or last triggered) is somehow ahead of 'now',
    // and it was picked by GetDueReminders, it means 'now' just passed it.
    // So, the next occurrence should be calculated from this 'baseTime'.

	// Simple rules from parseWhenSimple: "every X minutes/hours/days"
	re := regexp.MustCompile(`^every (\d+) (minutes|hours|days)$`)
	matches := re.FindStringSubmatch(strings.ToLower(rule))

	if len(matches) == 3 {
		value, err := strconv.Atoi(matches[1])
		if err != nil {
			// This should ideally not happen due to the regex \d+
			return time.Time{}, fmt.Errorf("internal error parsing number from rule '%s': %v", rule, err)
		}
		unit := matches[2]
		var durationToAdd time.Duration

		switch unit {
		case "minutes":
			durationToAdd = time.Duration(value) * time.Minute
		case "hours":
			durationToAdd = time.Duration(value) * time.Hour
		case "days":
			durationToAdd = time.Duration(value) * time.Hour * 24
		default:
			return time.Time{}, fmt.Errorf("unknown unit in recurrence rule: %s", unit)
		}

		next := baseTime.Add(durationToAdd)
        // Ensure the next calculated time is in the future from 'now'.
        // If baseTime.Add(duration) is still in the past (e.g. bot was offline for a long time),
        // keep adding the duration until it's in the future.
        for !next.After(now) {
            next = next.Add(durationToAdd)
        }
		return next, nil
	}

	return time.Time{}, fmt.Errorf("unsupported recurrence rule for auto-calculation: '%s'", rule)
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
