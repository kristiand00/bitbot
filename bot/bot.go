package bot

import (
	"bitbot/pb" // PocketBase interaction
	"bytes"
	"encoding/base64"
	"encoding/binary" // For PCM to byte conversion
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os" // Restoring os import
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync" // For RWMutex
	"time" // Added for timeout in receiveOpusPackets

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/zaf/resample"
	"google.golang.org/genai" // New GenAI import
	"layeh.com/gopus"         // Using layeh.com/gopus for Discord voice
)

var (
	// BotToken is injected at build time or via env
	BotToken      string
	GeminiAPIKey  string // This is already used by chat.go's InitGeminiClient
	CryptoToken   string
	AllowedUserID string
	AppId         string

	// Voice activity tracking
	activeSpeakers      = make(map[string]bool)
	activeSpeakersMutex sync.RWMutex
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
	discord.AddHandler(voiceStateUpdate) // Add voice state update handler
	discord.AddHandler(buttonHandler)    // Add button handler

	log.Info("Opening Discord connection...")
	err = discord.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer discord.Close()

	// Initialize PocketBase early
	log.Info("Initializing PocketBase...")
	pb.Init()
	log.Info("PocketBase initialized successfully.")

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
	UserID            string
	GuildID           string
	DiscordSession    *discordgo.Session
	OriginalChannelID string
	GenAISession      *genai.Chat
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

func voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	activeSpeakersMutex.Lock()
	defer activeSpeakersMutex.Unlock()

	if vs.SelfMute || vs.SelfDeaf {
		activeSpeakers[vs.UserID] = false
	} else {
		activeSpeakers[vs.UserID] = true
	}
}

func receiveOpusPackets(vc *discordgo.VoiceConnection, guildID string, originalChannelID string, dgSession *discordgo.Session) {
	log.Infof("Starting Opus packet receiver for guild %s (original channel %s)", guildID, originalChannelID)
	defer log.Infof("Stopping Opus packet receiver for guild %s (original channel %s)", guildID, originalChannelID)

	if vc == nil || vc.OpusRecv == nil {
		log.Errorf("Voice connection or OpusRecv channel is nil for guild %s. Cannot receive packets.", guildID)
		return
	}

	// Get the user ID from the voice connection
	userID := ""
	guild, err := dgSession.State.Guild(guildID)
	if err != nil {
		log.Errorf("Failed to get guild information: %v", err)
		return
	}

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == vc.ChannelID && vs.UserID != dgSession.State.User.ID {
			userID = vs.UserID
			break
		}
	}

	if userID == "" {
		log.Errorf("Could not find user ID for voice channel in guild %s", guildID)
		return
	}

	decoder, err := gopus.NewDecoder(48000, 1)
	if err != nil {
		log.Errorf("Failed to create Opus decoder: %v", err)
		return
	}
	log.Infof("opus decoder created for guild %s", guildID)
	const pcmFrameSize = 960

	// Buffer for accumulating audio data
	var audioBuffer []int16
	const bufferDuration = 3 * time.Second // Process every 3 seconds of audio
	const samplesPerSecond = 48000
	const bufferSize = 144000 // 3 seconds * 48000 samples per second
	lastProcessTime := time.Now()

	// Voice activity detection
	var silenceStart time.Time
	const silenceThreshold = 500 * time.Millisecond // Consider it silence after 500ms of no speech
	const minSpeechDuration = 1 * time.Second       // Ignore audio bursts shorter than 1 second
	var speechStart time.Time
	var isProcessingSpeech bool

	for {
		select {
		case packet, ok := <-vc.OpusRecv:
			if !ok {
				log.Warnf("OpusRecv channel closed for guild %s. Exiting receiver goroutine.", guildID)
				return
			}

			if packet == nil || packet.Opus == nil {
				log.Debugf("Received nil packet or nil Opus data for guild %s. Skipping.", guildID)
				continue
			}

			// Check if anyone is speaking in the channel
			activeSpeakersMutex.RLock()
			isSpeaking := false
			for _, speaking := range activeSpeakers {
				if speaking {
					isSpeaking = true
					break
				}
			}
			activeSpeakersMutex.RUnlock()

			if isSpeaking {
				if !isProcessingSpeech {
					speechStart = time.Now()
					isProcessingSpeech = true
				}
				silenceStart = time.Time{} // Reset silence timer
				pcm, err := decoder.Decode(packet.Opus, pcmFrameSize, false)
				if err != nil {
					log.Errorf("Failed to decode Opus packet: %v", err)
					continue
				}

				// Add decoded PCM data to buffer
				audioBuffer = append(audioBuffer, pcm...)

				// Process buffer if it's full or enough time has passed
				if len(audioBuffer) >= bufferSize || time.Since(lastProcessTime) >= bufferDuration {
					if len(audioBuffer) > 0 {
						processAudioBuffer(audioBuffer, guildID, userID, dgSession)
						audioBuffer = nil
						lastProcessTime = time.Now()
					}
				}
			} else {
				if isProcessingSpeech {
					if silenceStart.IsZero() {
						silenceStart = time.Now()
					} else if time.Since(silenceStart) > silenceThreshold {
						// Only process if the speech duration was long enough
						if time.Since(speechStart) >= minSpeechDuration {
							// Process any remaining audio in the buffer
							if len(audioBuffer) > 0 {
								processAudioBuffer(audioBuffer, guildID, userID, dgSession)
							}
						} else {
							log.Debugf("Ignoring short audio burst of %v", time.Since(speechStart))
						}
						audioBuffer = nil
						lastProcessTime = time.Now()
						isProcessingSpeech = false
					}
				}
			}

		case <-time.After(30 * time.Second):
			// log.Debugf("No Opus packet received for 30s in guild %s. Still listening...", guildID)
		}
	}
}

// Helper function to process audio buffer
func processAudioBuffer(audioBuffer []int16, guildID string, userID string, dgSession *discordgo.Session) {
	// Convert PCM to WAV format
	wavBuffer := new(bytes.Buffer)

	// Write WAV header
	// RIFF header
	wavBuffer.WriteString("RIFF")
	binary.Write(wavBuffer, binary.LittleEndian, uint32(36+len(audioBuffer)*2)) // File size - 8
	wavBuffer.WriteString("WAVE")

	// Format chunk
	wavBuffer.WriteString("fmt ")
	binary.Write(wavBuffer, binary.LittleEndian, uint32(16))      // Chunk size
	binary.Write(wavBuffer, binary.LittleEndian, uint16(1))       // Audio format (1 for PCM)
	binary.Write(wavBuffer, binary.LittleEndian, uint16(1))       // Number of channels
	binary.Write(wavBuffer, binary.LittleEndian, uint32(48000))   // Sample rate
	binary.Write(wavBuffer, binary.LittleEndian, uint32(48000*2)) // Byte rate
	binary.Write(wavBuffer, binary.LittleEndian, uint16(2))       // Block align
	binary.Write(wavBuffer, binary.LittleEndian, uint16(16))      // Bits per sample

	// Data chunk
	wavBuffer.WriteString("data")
	binary.Write(wavBuffer, binary.LittleEndian, uint32(len(audioBuffer)*2)) // Data size

	// Write PCM data
	for _, sample := range audioBuffer {
		binary.Write(wavBuffer, binary.LittleEndian, int16(sample))
	}

	// Create request body for REST API
	req := AudioRequest{
		Contents: []struct {
			Parts []struct {
				Text       string `json:"text,omitempty"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData,omitempty"`
			} `json:"parts"`
		}{
			{
				Parts: []struct {
					Text       string `json:"text,omitempty"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData,omitempty"`
				}{
					{
						InlineData: &struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						}{
							MimeType: "audio/wav",
							Data:     base64.StdEncoding.EncodeToString(wavBuffer.Bytes()),
						},
					},
				},
			},
		},
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		log.Errorf("Failed to marshal request: %v", err)
		return
	}

	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", AudioModelName, geminiAPIKey)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Errorf("Failed to make request: %v", err)
		return
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Errorf("Audio model request failed with status %d: %s", resp.StatusCode, string(body))
		return
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text       string `json:"text,omitempty"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		resp.Body.Close()
		log.Errorf("Failed to decode response: %v", err)
		return
	}
	resp.Body.Close()

	// Create a DM channel with the user
	channel, err := dgSession.UserChannelCreate(userID)
	if err != nil {
		log.Errorf("Failed to create DM channel with user %s: %v", userID, err)
		return
	}

	// Process the response
	for _, candidate := range result.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				// Send text response to user's DM
				_, err := dgSession.ChannelMessageSend(channel.ID, part.Text)
				if err != nil {
					log.Errorf("Failed to send text response to user DM: %v", err)
				}
			}
			if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/") {
				audioData, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					log.Errorf("Failed to decode audio data: %v", err)
					continue
				}
				go processAndSendDiscordAudioResponse(dgSession, guildID, userID, audioData, 24000)
			}
		}
	}
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

	encoder, err := gopus.NewEncoder(48000, 1, gopus.Audio)
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

		opusData, err := encoder.Encode(targetPcmSlice, pcmFrameSamples, 2048)
		if err != nil {
			log.Errorf("Failed to encode PCM: %v", err)
			return
		}
		if len(opusData) > 0 {
			select {
			case vc.OpusSend <- opusData:
				totalSentBytes += len(opusData)
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
			}
		}
		userVoiceSessions = make(map[string]*UserVoiceSession)
		log.Info("Cleared all GenAI user voice sessions.")
	} else {
		log.Info("No active GenAI user voice sessions to cleanup.")
	}
	log.Info("All voice sessions cleanup complete.")
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
		resampled, err := resampleAudio(pcmFloat64Input, inputSampleRateHz, discordTargetSampleRate)
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

	encoder, err := createOpusEncoder(discordTargetSampleRate)
	if err != nil {
		log.Errorf("Failed to create Opus encoder for TTS response (user %s, guild %s): %v", userID, guildID, err)
		return
	}

	if err := vc.Speaking(true); err != nil {
		log.Errorf("Failed to set speaking true for TTS response (user %s, guild %s): %v", userID, guildID, err)
	}

	const pcmFrameSamples = 960
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

		opusData, err := encoder.Encode(pcmFrameCurrent, pcmFrameSamples, 2048)
		if err != nil {
			log.Errorf("Failed to encode PCM: %v", err)
			return
		}
		if len(opusData) > 0 {
			select {
			case vc.OpusSend <- opusData:
				totalOpusBytesSent += len(opusData)
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
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d minutes", value)
			}
		case "h":
			duration *= time.Hour
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d hours", value)
			}
		case "d":
			duration *= time.Hour * 24
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d days", value)
			}
		default:
			return time.Time{}, false, "", fmt.Errorf("unknown time unit: %s", unit)
		}

		if isRecurring && recurrenceRule == "" { // Should be set by above cases
			return time.Time{}, false, "", fmt.Errorf("could not determine recurrence rule for: %s", whenStr)
		}

		return now.Add(duration), isRecurring, recurrenceRule, nil
	}

	// Enhanced parsing for more natural time formats
	// Handle "tomorrow at Xam/pm" format
	if strings.HasPrefix(whenStr, "tomorrow at ") {
		timePart := strings.TrimPrefix(whenStr, "tomorrow at ")
		parsedTime, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time format in 'tomorrow at %s': %v", timePart, err)
		}

		tomorrow := now.AddDate(0, 0, 1)
		result := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(),
			parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())

		return result, isRecurring, recurrenceRule, nil
	}

	// Handle "next [day] at Xam/pm" format
	if strings.HasPrefix(whenStr, "next ") && strings.Contains(whenStr, " at ") {
		parts := strings.SplitN(whenStr, " at ", 2)
		if len(parts) != 2 {
			return time.Time{}, false, "", fmt.Errorf("invalid format for 'next [day] at [time]': %s", whenStr)
		}

		dayPart := strings.TrimPrefix(parts[0], "next ")
		timePart := parts[1]

		parsedTime, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time format: %v", err)
		}

		nextDay, err := parseNextDay(dayPart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid day format: %v", err)
		}

		result := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(),
			parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())

		return result, isRecurring, recurrenceRule, nil
	}

	// Handle "every day at Xam/pm" format
	if strings.HasPrefix(whenStr, "day at ") {
		timePart := strings.TrimPrefix(whenStr, "day at ")
		parsedTime, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time format in 'every day at %s': %v", timePart, err)
		}

		// Calculate next occurrence of this time today or tomorrow
		today := time.Date(now.Year(), now.Month(), now.Day(),
			parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())

		if today.Before(now) {
			today = today.AddDate(0, 0, 1) // Move to tomorrow if time has passed today
		}

		recurrenceRule = "every day"
		return today, true, recurrenceRule, nil
	}

	// Handle "every [weekday] at Xam/pm" format
	if strings.Contains(whenStr, " at ") {
		parts := strings.SplitN(whenStr, " at ", 2)
		if len(parts) == 2 {
			dayPart := parts[0]
			timePart := parts[1]

			parsedTime, err := parseTimeOfDay(timePart)
			if err != nil {
				return time.Time{}, false, "", fmt.Errorf("invalid time format: %v", err)
			}

			nextDay, err := parseNextDay(dayPart)
			if err != nil {
				return time.Time{}, false, "", fmt.Errorf("invalid day format: %v", err)
			}

			result := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(),
				parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())

			recurrenceRule = fmt.Sprintf("every %s", dayPart)
			return result, true, recurrenceRule, nil
		}
	}

	// For now, only "in Xm/h/d" is supported for non-recurring.
	// And "every Xm/h/d" for recurring.
	if isRecurring {
		return time.Time{}, false, "", fmt.Errorf("unsupported recurring format: '%s'. Try 'every Xm/Xh/Xd' or 'every day at Xam/pm'", whenStr)
	}
	return time.Time{}, false, "", fmt.Errorf("unsupported time format: '%s'. Try 'in Xm/Xh/Xd', 'tomorrow at Xam/pm', or 'next [day] at Xam/pm'", whenStr)
}

// parseTimeOfDay parses time strings like "10am", "3:30pm", "14:30"
func parseTimeOfDay(timeStr string) (time.Time, error) {
	timeStr = strings.ToLower(strings.TrimSpace(timeStr))

	// Handle 12-hour format with am/pm
	if strings.Contains(timeStr, "am") || strings.Contains(timeStr, "pm") {
		// Check if it's pm before removing it
		isPM := strings.Contains(timeStr, "pm")

		// Remove am/pm and parse
		timeStr = strings.ReplaceAll(timeStr, "am", "")
		timeStr = strings.ReplaceAll(timeStr, "pm", "")
		timeStr = strings.TrimSpace(timeStr)

		// Parse the time
		var hour, minute int
		if strings.Contains(timeStr, ":") {
			parts := strings.Split(timeStr, ":")
			if len(parts) != 2 {
				return time.Time{}, fmt.Errorf("invalid time format: %s", timeStr)
			}
			var err error
			hour, err = strconv.Atoi(parts[0])
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid hour: %s", parts[0])
			}
			minute, err = strconv.Atoi(parts[1])
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid minute: %s", parts[1])
			}
		} else {
			var err error
			hour, err = strconv.Atoi(timeStr)
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid hour: %s", timeStr)
			}
			minute = 0
		}

		// Handle 12-hour format
		if isPM && hour != 12 {
			hour += 12
		} else if !isPM && hour == 12 {
			hour = 0
		}

		if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return time.Time{}, fmt.Errorf("time out of range: %02d:%02d", hour, minute)
		}

		return time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC), nil
	}

	// Handle 24-hour format
	if strings.Contains(timeStr, ":") {
		parts := strings.Split(timeStr, ":")
		if len(parts) != 2 {
			return time.Time{}, fmt.Errorf("invalid time format: %s", timeStr)
		}

		hour, err := strconv.Atoi(parts[0])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid hour: %s", parts[0])
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid minute: %s", parts[1])
		}

		if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return time.Time{}, fmt.Errorf("time out of range: %02d:%02d", hour, minute)
		}

		return time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC), nil
	}

	return time.Time{}, fmt.Errorf("unsupported time format: %s", timeStr)
}

// parseNextDay parses day strings like "monday", "tuesday", etc.
func parseNextDay(dayStr string) (time.Time, error) {
	dayStr = strings.ToLower(strings.TrimSpace(dayStr))

	dayMap := map[string]time.Weekday{
		"monday":    time.Monday,
		"mon":       time.Monday,
		"tuesday":   time.Tuesday,
		"tue":       time.Tuesday,
		"wednesday": time.Wednesday,
		"wed":       time.Wednesday,
		"thursday":  time.Thursday,
		"thu":       time.Thursday,
		"friday":    time.Friday,
		"fri":       time.Friday,
		"saturday":  time.Saturday,
		"sat":       time.Saturday,
		"sunday":    time.Sunday,
		"sun":       time.Sunday,
	}

	targetDay, exists := dayMap[dayStr]
	if !exists {
		return time.Time{}, fmt.Errorf("unknown day: %s", dayStr)
	}

	now := time.Now()
	currentDay := now.Weekday()

	// Calculate days until next occurrence
	daysUntil := int(targetDay - currentDay)
	if daysUntil <= 0 {
		daysUntil += 7 // Move to next week
	}

	return now.AddDate(0, 0, daysUntil), nil
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

	timeFormat := "Jan 2, 2006 at 3:04 PM MST"
	var components []discordgo.MessageComponent
	var response strings.Builder
	response.WriteString("**Your active reminders:**\n")

	for idx, r := range reminders {
		var nextDue time.Time
		if r.IsRecurring {
			nextDue = r.NextReminderTime
		} else {
			nextDue = r.ReminderTime
		}

		var nextDueStr string
		if !nextDue.IsZero() {
			nextDueStr = nextDue.Local().Format(timeFormat)
		} else {
			nextDueStr = "N/A (Error in time)"
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

		// Add a delete button for this reminder
		components = append(components, &discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.Button{
					Label:    "Delete",
					CustomID: fmt.Sprintf("reminder_delete_%s", r.ID),
					Style:    discordgo.DangerButton,
				},
			},
		})
	}

	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    response.String(),
			Components: components,
		},
	})
	if err != nil {
		log.Errorf("Failed to send reminders list with buttons: %v", err)
	}
}

// Add a handler for button interactions to delete reminders
func buttonHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionMessageComponent {
		customID := i.MessageComponentData().CustomID
		if strings.HasPrefix(customID, "reminder_delete_") {
			reminderID := strings.TrimPrefix(customID, "reminder_delete_")
			userID := i.Member.User.ID
			reminder, err := pb.GetReminderByID(reminderID)
			if err != nil {
				respondWithMessage(s, i, "Could not find the reminder to delete.")
				return
			}
			if reminder.UserID != userID {
				respondWithMessage(s, i, "You can only delete reminders you created.")
				return
			}
			err = pb.DeleteReminder(reminderID)
			if err != nil {
				respondWithMessage(s, i, "Failed to delete the reminder. Please try again.")
				return
			}
			respondWithMessage(s, i, "Reminder deleted successfully.")
		}
	}
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
// Enhanced to parse more complex RecurrenceRule (e.g., "every day", "every monday").
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

	rule = strings.ToLower(strings.TrimSpace(rule))

	// Handle "every day" format
	if rule == "every day" {
		// Extract the time from the original reminder time
		hour := originalReminderTime.Hour()
		minute := originalReminderTime.Minute()

		// Calculate next occurrence
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())

		// If the time has already passed today, move to tomorrow
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}

		return next, nil
	}

	// Handle "every [weekday]" format
	if strings.HasPrefix(rule, "every ") {
		dayPart := strings.TrimPrefix(rule, "every ")
		dayMap := map[string]time.Weekday{
			"monday":    time.Monday,
			"mon":       time.Monday,
			"tuesday":   time.Tuesday,
			"tue":       time.Tuesday,
			"wednesday": time.Wednesday,
			"wed":       time.Wednesday,
			"thursday":  time.Thursday,
			"thu":       time.Thursday,
			"friday":    time.Friday,
			"fri":       time.Friday,
			"saturday":  time.Saturday,
			"sat":       time.Saturday,
			"sunday":    time.Sunday,
			"sun":       time.Sunday,
		}

		targetDay, exists := dayMap[dayPart]
		if exists {
			// Extract the time from the original reminder time
			hour := originalReminderTime.Hour()
			minute := originalReminderTime.Minute()

			// Calculate next occurrence of this weekday
			currentDay := now.Weekday()
			daysUntil := int(targetDay - currentDay)
			if daysUntil <= 0 {
				daysUntil += 7 // Move to next week
			}

			next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
			next = next.AddDate(0, 0, daysUntil)

			return next, nil
		}
	}

	// Simple rules from parseWhenSimple: "every X minutes/hours/days"
	re := regexp.MustCompile(`^every (\d+) (minutes|hours|days)$`)
	matches := re.FindStringSubmatch(rule)

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

func createOpusEncoder(sampleRate int) (*gopus.Encoder, error) {
	return gopus.NewEncoder(sampleRate, 1, gopus.Audio)
}

func resampleAudio(input []float64, fromRate, toRate int) ([]float64, error) {
	// Convert float64 to int16 PCM
	pcmData := make([]int16, len(input))
	for i, v := range input {
		pcmData[i] = int16(v * 32767.0)
	}

	// Convert int16 to bytes
	pcmBytes := make([]byte, len(pcmData)*2)
	for i, v := range pcmData {
		binary.LittleEndian.PutUint16(pcmBytes[i*2:], uint16(v))
	}

	// Create a buffer for the output
	var outputBuf bytes.Buffer

	// Create a resampler
	r, err := resample.New(&outputBuf, float64(fromRate), float64(toRate), 1, 2, resample.Quick)
	if err != nil {
		return nil, err
	}

	// Write the input data
	if _, err := r.Write(pcmBytes); err != nil {
		return nil, err
	}

	// Close the resampler
	if err := r.Close(); err != nil {
		return nil, err
	}

	// Convert the output back to float64
	outputBytes := outputBuf.Bytes()
	output := make([]float64, len(outputBytes)/2)
	for i := 0; i < len(outputBytes); i += 2 {
		output[i/2] = float64(int16(binary.LittleEndian.Uint16(outputBytes[i:]))) / 32767.0
	}

	return output, nil
}
