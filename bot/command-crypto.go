package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	message = strings.ToUpper(strings.TrimSpace(message)) // convert to uppercase and remove leading/trailing spaces
	message = strings.ReplaceAll(message, "!CRY ", "")    // remove "!cry" prefix
	currency := message[:]

	if len(currency) < 3 || len(currency) > 5 { // check length of currency code
		return &discordgo.MessageSend{
			Content: "Sorry, cant recognize crypto currency shortcode try uppercase and with length between 3 to 5",
		}
	}

	cryptoURL := fmt.Sprintf("%s?fsym=%s&tsyms=USD&api_key=%s", URL, currency, CryptoToken)
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
			Description: "Price for " + currency,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "1 " + currency,
					Value:  usd + " USD",
					Inline: true,
				},
			},
		},
		},
	}
	return embed
}
