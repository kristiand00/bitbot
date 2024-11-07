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
	openAIToken, ok := os.LookupEnv("OPENAI_TOKEN")
	if !ok {
		log.Fatal("Must set OpenAI token as env variable: OPENAI_TOKEN")
	}
	AllowedUserID, ok := os.LookupEnv("ADMIN_DISCORD_ID")
	if !ok {
		log.Fatal("Must set OpenAI token as env variable: ADMIN_DISCORD_ID")
	}

	bot.BotToken = botToken
	bot.CryptoToken = cryptoToken
	bot.AppId = appId
	bot.OpenAIToken = openAIToken
	bot.AllowedUserID = AllowedUserID

	bot.Run()
}
