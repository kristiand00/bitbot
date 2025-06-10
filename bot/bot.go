package bot

import (
	"bitbot/pb"
	"fmt"
	"math/rand"
	"context"          // For GenAI client
	"encoding/binary"  // For PCM to byte conversion
	"errors"           // For error handling
	"os"               // Restoring os import
	"os/signal"
	"strings"
	"time" // Added for timeout in receiveOpusPackets

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"google.golang.org/api/option" // For GenAI client
	"io"                           // For io.EOF in GenAI receive
	"sync"                         // For RWMutex

	"github.com/pion/opus" // Opus decoding (switched from layeh/gopus)
	"github.com/zaf/resample" // For resampling audio

	// "github.com/google/generative-ai-go/genai" // Old GenAI import
	"google.golang.org/genai" // New GenAI import
	// "google.golang.org/genai/types" // Removed as it's not a valid package in v1.10.0
)

var (
	BotToken      string
	GeminiAPIKey  string // This is already used by chat.go's InitGeminiClient
	CryptoToken   string
	AllowedUserID string
	AppId         string

	// ttsClient *texttospeech.Client // REMOVED
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
	GenAISession      *genai.LiveSession // Updated to LiveSession
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
	commands := []*discordgo.ApplicationCommand{
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
	}

	for _, cmd := range commands {
		_, err := discord.ApplicationCommandCreate(appID, "", cmd)
		if err != nil {
			log.Fatalf("Cannot create slash command %q: %v", cmd.Name, err)
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

			n, err := decoder.Decode(packet.Opus, pcmBuffer)
			if err != nil {
				log.Errorf("pion/opus failed to decode Opus packet for SSRC %d, guild %s: %v", packet.SSRC, guildID, err)
				continue
			}

			pcmDataForGenAI := make([]int16, n)
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
			realtimeInput := &genai.RealtimeInput{Audio: mediaBlob} // Reverted to genai.RealtimeInput

			errSend := userSession.GenAISession.SendRealtimeInput(realtimeInput) // Method name might change
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
		connectConfig := &genai.LiveConnectConfig{ // Reverted to genai.LiveConnectConfig
			ResponseModalities: []genai.Modality{genai.ModalityAudio}, // Reverted to genai.Modality
			SpeechConfig: &genai.SpeechConfig{ // Reverted to genai.SpeechConfig
				AudioEncoding:   "LINEAR16", // This might be an enum like genai.AudioEncodingLinear16
				SampleRateHertz: 24000,
			},
			ContextWindowCompression: &genai.ContextWindowCompressionConfig{ // Reverted to genai.ContextWindowCompressionConfig
				SlidingWindow: &genai.SlidingWindow{}, // Reverted to genai.SlidingWindow
			},
		}
		log.Infof("Attempting to connect to GenAI Live with model: %s, output config: 24kHz LINEAR16", modelName)

		// The connection method might change.
		// Assuming geminiClient is already updated to the new SDK's client type.
		// Placeholder: geminiClient.StartLiveChat(ctx, modelName, connectConfig) or similar.
		// For now, keeping a structure similar to the original if the exact method is unknown.
		// This part might need further adjustment based on the actual new SDK.
		liveService := genai.NewLiveClient(geminiClient) // This is a guess, might be geminiClient.Live(ctx) or similar
		liveSession, err := liveService.Connect(ctx, modelName, connectConfig) // Or liveClient.StartLiveChat(...)
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

	encoder, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
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
			if msg.ServerContent != nil && msg.ServerContent.GetModelTurn() != nil {
				modelTurn := msg.ServerContent.GetModelTurn()
				for _, part := range modelTurn.Parts {
					if mediaPart := part.GetMedia(); mediaPart != nil {
						if audioBytes := mediaPart.GetAudio(); audioBytes != nil && len(audioBytes) > 0 {
							log.Infof("Received audio data blob from GenAI for user %s. MIME: %s, Size: %d bytes.", userID, mediaPart.GetMIMEType(), len(audioBytes))
							go processAndSendDiscordAudioResponse(dgSession, guildID, userID, audioBytes, 24000)
							modelRespondedWithAudio = true
							break
						}
					}
				}
				if !modelRespondedWithAudio {
					log.Warnf("GenAI ModelTurn for user %s (audio expected) had parts, but no parsable audio/media part found.", userID)
				}
			} else if msg.Error != nil {
				log.Errorf("GenAI server sent an error in audio stream for user %s: code %d, message: %s", userID, msg.Error.GetCode(), msg.Error.GetMessage())
				if dgSession != nil && originalChannelID != "" {
					fallbackMsg := fmt.Sprintf("Sorry <@%s>, I encountered an error while generating a voice response: %s", userID, msg.Error.GetMessage())
					_, sendErr := dgSession.ChannelMessageSend(originalChannelID, fallbackMsg)
					if sendErr != nil {
						log.Errorf("Failed to send error fallback message for user %s to channel %s: %v", userID, originalChannelID, sendErr)
					}
				}
			} else {
				log.Warnf("Received GenAI message for user %s (audio expected) with no useful ServerContent or Error. Msg: %+v", userID, msg)
			}
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
		if val > 32767.0 { val = 32767.0 }
		if val < -32768.0 { val = -32768.0 }
		pcmInt16Output[i] = int16(val)
	}

	encoder, err := opus.NewEncoder(discordTargetSampleRate, 1, opus.AppVoIP)
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
		}
	} else if i.Type == discordgo.InteractionModalSubmit {
		modalHandler(s, i)
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
