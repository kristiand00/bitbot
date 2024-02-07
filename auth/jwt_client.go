// auth/jwt_client.go
package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// JWTClient is a struct representing the JWT authentication client.
type JWTClient struct {
	ServerURL     string
	TokenEndpoint string
	ClientID      string
	ClientSecret  string
}

// NewJWTClient creates a new JWTClient instance.
func NewJWTClient(serverURL, tokenEndpoint, clientID, clientSecret string) *JWTClient {
	return &JWTClient{
		ServerURL:     serverURL,
		TokenEndpoint: tokenEndpoint,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
	}
}

// GetAccessToken retrieves a JWT access token from the server.
func (c *JWTClient) GetAccessToken() (string, error) {
	// Construct the payload for token request
	payload := map[string]string{
		"client_id":     c.ClientID,
		"client_secret": c.ClientSecret,
	}

	// Convert payload to JSON
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	// Make a POST request to the /auth/tokens endpoint
	resp, err := http.Post(c.ServerURL+c.TokenEndpoint, "application/json", bytes.NewBuffer(payloadJSON))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to authenticate: %s", response)
	}

	// Return the obtained token
	return string(response), nil
}

// MakeRequest makes an authenticated HTTP request using the provided access token.
func (c *JWTClient) MakeRequest(apiURL, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.ServerURL+"/auth/tokens/make-request", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return response, nil
}
