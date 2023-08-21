package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const URL string = "https://min-api.cryptocompare.com/data/price"

type CryptoData struct {
	Usd float64 `json:"USD"`
}

func getCurrentCryptoPrice(message string) *discordgo.MessageSend {
	r, _ := regexp.Compile(`\s?[A-Z]{3,5}\s?`)
	currency := r.FindString(message)
	curr := strings.ReplaceAll(currency, " ", "")

	if curr == "" {
		return &discordgo.MessageSend{
			Content: "Sorry, cant recognize crypto currency shortcode try uppercase and with spaces around",
		}
	}

	cryptoURL := fmt.Sprintf("%s?fsym=%s&tsyms=USD&api_key=%s", URL, curr, CryptoToken)
	fmt.Println(cryptoURL)
	client := http.Client{Timeout: 5 * time.Second}

	response, err := client.Get(cryptoURL)
	if err != nil {
		return &discordgo.MessageSend{
			Content: "Sorry, there was an error trying to connect to api",
		}
	}

	body, _ := io.ReadAll(response.Body)
	defer response.Body.Close()

	var data CryptoData
	json.Unmarshal([]byte(body), &data)

	fmt.Println(body, data, data.Usd)
	usd := strconv.FormatFloat(data.Usd, 'f', 2, 64)

	embed := &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Type:        discordgo.EmbedTypeRich,
			Title:       "Current Price",
			Description: "Price for " + curr,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "1 " + curr,
					Value:  usd + " USD",
					Inline: true,
				},
			},
		},
		},
	}
	return embed
}
