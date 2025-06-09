package main

import (
	"bitbot/bot"
	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"os"
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

	bot.Run()
}
