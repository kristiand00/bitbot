package main

import (
	"bitbot/bot"
	"os"
	"os/signal" // Required for signal.Notify
	"syscall"   // Required for syscall.SIGINT, syscall.SIGTERM

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("no .env file found")
	}

}

func main() {
	botToken, ok := os.LookupEnv("BOT_TOKEN")
	if !ok {
		log.Fatal("Must set Discord token asn env variable: BOT_TOKEN")

	}
	cryptoToken, ok := os.LookupEnv("CRYPTO_TOKEN")
	if !ok {
		log.Fatal("Must set crypto token as env variable: CRYPTO_TOKEN")
	}
	appId, ok := os.LookupEnv("APP_ID")
	if !ok {
		log.Fatal("Must set appId as env variable: APP_ID")
	}
	geminiAPIKey, ok := os.LookupEnv("GEMINI_API_KEY")
	if !ok {
		log.Fatal("Must set Gemini API key as env variable: GEMINI_API_KEY")
	}
	AllowedUserID, ok := os.LookupEnv("ADMIN_DISCORD_ID")
	if !ok {
		log.Fatal("Must set OpenAI token as env variable: ADMIN_DISCORD_ID")
	}

	bot.BotToken = botToken
	bot.CryptoToken = cryptoToken
	bot.AppId = appId
	bot.GeminiAPIKey = geminiAPIKey
	bot.AllowedUserID = AllowedUserID

	// Setup signal handling for graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	log.Info("Bot starting... Press CTRL+C to exit.")
	go bot.Run() // Run the bot in a goroutine so we can listen for the stop signal

	<-stop // Wait for SIGINT or SIGTERM

	log.Info("Shutting down bot...")
	bot.CleanupAllVoiceSessions() // Call the cleanup function
	// Any other global cleanup can go here (e.g. closing Discord session if not handled by bot.Run defer)
	log.Info("Bot shutdown complete.")
}
