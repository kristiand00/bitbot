package bot

import (
	"bitbot/pb"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pocketbase/pocketbase/core"
)

// OAuth linking flow for MCP servers whose auth_mode is "oauth". Each user
// authorizes in a browser; the resulting session is per user. The bot bridges
// the browser redirect back to the waiting authorization flow via a callback
// endpoint keyed by the OAuth `state` parameter.

const oauthAuthorizeTimeout = 5 * time.Minute

// authResult is delivered by the callback endpoint to a waiting flow.
type authResult struct {
	code  string
	state string
	err   error
}

var (
	pendingAuths   = map[string]chan authResult{} // keyed by OAuth state
	pendingAuthsMu sync.Mutex
)

func registerPendingAuth(state string) chan authResult {
	ch := make(chan authResult, 1)
	pendingAuthsMu.Lock()
	pendingAuths[state] = ch
	pendingAuthsMu.Unlock()
	return ch
}

func unregisterPendingAuth(state string) {
	pendingAuthsMu.Lock()
	delete(pendingAuths, state)
	pendingAuthsMu.Unlock()
}

func deliverAuth(state string, res authResult) bool {
	pendingAuthsMu.Lock()
	ch := pendingAuths[state]
	pendingAuthsMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- res:
		return true
	default:
		return false
	}
}

// oauthRedirectBase returns the public base URL the OAuth provider redirects
// back to (e.g. https://bot.example.com). Required for OAuth servers.
func oauthRedirectBase() string {
	return strings.TrimRight(os.Getenv("OAUTH_REDIRECT_BASE"), "/")
}

func oauthRedirectURL() string {
	base := oauthRedirectBase()
	if base == "" {
		return ""
	}
	return base + "/oauth/callback"
}

// RegisterOAuthRoutes binds the OAuth callback endpoint on the PocketBase HTTP
// server. Call before app.Start(). The callback delivers the authorization code
// to the waiting flow, matched by the `state` parameter.
func RegisterOAuthRoutes(app core.App) {
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		e.Router.GET("/oauth/callback", func(re *core.RequestEvent) error {
			q := re.Request.URL.Query()
			state := q.Get("state")
			var res authResult
			if errStr := q.Get("error"); errStr != "" {
				res = authResult{state: state, err: fmt.Errorf("authorization error: %s", errStr)}
			} else {
				res = authResult{code: q.Get("code"), state: state}
			}
			if !deliverAuth(state, res) {
				return re.String(400, "No matching authorization request (it may have expired). You can close this window.")
			}
			return re.String(200, "Authorization complete. You can close this window and return to Discord.")
		})
		return e.Next()
	})
	log.Info("OAuth callback route registered at /oauth/callback")
}

// makeAuthorizationCodeFetcher DMs the user the authorize URL and waits for the
// browser redirect to arrive at the callback endpoint.
func makeAuthorizationCodeFetcher(session *discordgo.Session, userID, serverName string) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		state := extractState(args.URL)
		if state == "" {
			return nil, fmt.Errorf("authorization URL missing state parameter")
		}
		ch := registerPendingAuth(state)
		defer unregisterPendingAuth(state)

		if dm, err := session.UserChannelCreate(userID); err == nil {
			session.ChannelMessageSend(dm.ID, fmt.Sprintf("To connect **%s**, authorize access here (link expires in %d minutes):\n%s", serverName, int(oauthAuthorizeTimeout.Minutes()), args.URL))
		} else {
			return nil, fmt.Errorf("could not DM you the authorization link: %w", err)
		}

		select {
		case res := <-ch:
			if res.err != nil {
				return nil, res.err
			}
			return &auth.AuthorizationResult{Code: res.code, State: res.state}, nil
		case <-time.After(oauthAuthorizeTimeout):
			return nil, fmt.Errorf("authorization timed out")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func extractState(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("state")
}

// newOAuthHandler builds a per-user OAuth handler using Dynamic Client
// Registration, so no per-provider app registration is needed.
func newOAuthHandler(session *discordgo.Session, userID, serverName string) (*auth.AuthorizationCodeHandler, error) {
	redirect := oauthRedirectURL()
	if redirect == "" {
		return nil, fmt.Errorf("OAUTH_REDIRECT_BASE is not set; required for OAuth MCP servers")
	}
	return auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs: []string{redirect},
				ClientName:   "bitbot",
			},
		},
		RedirectURL:              redirect,
		AuthorizationCodeFetcher: makeAuthorizationCodeFetcher(session, userID, serverName),
	})
}

// linkOAuthServer runs the OAuth flow for the given server and, on success,
// connects it and registers its tools (owned by the linking user). It blocks
// until the flow completes or fails.
func linkOAuthServer(ctx context.Context, session *discordgo.Session, userID string, srv *pb.MCPServer) error {
	handler, err := newOAuthHandler(session, userID, srv.Name)
	if err != nil {
		return err
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		OAuthHandler:         handler,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bitbot", Version: "1.0.0"}, nil)

	// Connect triggers the OAuth flow (via the fetcher) if the server requires it.
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		cs.Close()
		return err
	}

	registerServerTools(srv, cs, res)

	// Best-effort: persist the acquired token so it can be reused later.
	if ts, terr := handler.TokenSource(ctx); terr == nil && ts != nil {
		if tok, gerr := ts.Token(); gerr == nil && tok != nil {
			_ = pb.SaveUserToken(&pb.UserToken{
				UserID:       userID,
				Server:       serverKey(srv.Owner, srv.Name),
				AccessToken:  tok.AccessToken,
				RefreshToken: tok.RefreshToken,
				TokenType:    tok.TokenType,
				Expiry:       tok.Expiry,
			})
		}
	}
	return nil
}
