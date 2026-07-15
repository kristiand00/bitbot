package pb

import (
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// MCP authentication modes.
const (
	MCPAuthBearer = "bearer" // static bearer token stored on the server row
	MCPAuthOAuth  = "oauth"  // per-user OAuth 2.1 (authorization code) tokens
)

const oauthTokensCollection = "oauth_tokens"

func normalizeAuthMode(m string) string {
	if m == MCPAuthOAuth {
		return MCPAuthOAuth
	}
	return MCPAuthBearer
}

// UserToken is a per-user OAuth token for a specific MCP server (identified by
// its serverKey).
type UserToken struct {
	UserID       string
	Server       string
	AccessToken  string
	RefreshToken string
	TokenType    string
	Expiry       time.Time
	Scope        string
}

// GetUserToken returns the stored token for a (user, server), or nil if none.
func GetUserToken(userID, server string) (*UserToken, error) {
	record, err := findUserToken(userID, server)
	if err != nil || record == nil {
		return nil, err
	}
	expiry, _ := time.Parse(time.RFC3339, record.GetString("expiry"))
	return &UserToken{
		UserID:       userID,
		Server:       server,
		AccessToken:  record.GetString("access_token"),
		RefreshToken: record.GetString("refresh_token"),
		TokenType:    record.GetString("token_type"),
		Expiry:       expiry,
		Scope:        record.GetString("scope"),
	}, nil
}

// SaveUserToken upserts a per-user token (create or update the existing row).
func SaveUserToken(t *UserToken) error {
	app := GetApp()
	record, err := findUserToken(t.UserID, t.Server)
	if err != nil {
		return err
	}
	if record == nil {
		collection, cerr := app.FindCollectionByNameOrId(oauthTokensCollection)
		if cerr != nil {
			return cerr
		}
		record = core.NewRecord(collection)
		record.Set("user_id", t.UserID)
		record.Set("server", t.Server)
	}
	record.Set("access_token", t.AccessToken)
	record.Set("refresh_token", t.RefreshToken)
	record.Set("token_type", t.TokenType)
	if t.Expiry.IsZero() {
		record.Set("expiry", "")
	} else {
		record.Set("expiry", t.Expiry.UTC().Format(time.RFC3339))
	}
	record.Set("scope", t.Scope)
	return app.Save(record)
}

// DeleteUserToken removes a user's token for a server (unlink). Not-found is a no-op.
func DeleteUserToken(userID, server string) error {
	record, err := findUserToken(userID, server)
	if err != nil || record == nil {
		return err
	}
	return GetApp().Delete(record)
}

func findUserToken(userID, server string) (*core.Record, error) {
	record, err := GetApp().FindFirstRecordByFilter(
		oauthTokensCollection, "user_id = {:u} && server = {:s}",
		dbx.Params{"u": userID, "s": server},
	)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}
