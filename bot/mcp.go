package bot

import (
	"bitbot/pb"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpReconcileInterval is how often the bot re-syncs its live MCP connections
// with the mcp_servers collection, so servers added/removed/changed are picked
// up without a restart.
const mcpReconcileInterval = 60 * time.Second

// mcpVisibilityChoices are the selectable visibility levels for /mcp add|access.
var mcpVisibilityChoices = []*discordgo.ApplicationCommandOptionChoice{
	{Name: "private (only you)", Value: pb.MCPVisibilityPrivate},
	{Name: "admins", Value: pb.MCPVisibilityAdmins},
	{Name: "public (everyone)", Value: pb.MCPVisibilityPublic},
}

// mcpAuthModeChoices are the selectable auth modes for /mcp add.
var mcpAuthModeChoices = []*discordgo.ApplicationCommandOptionChoice{
	{Name: "bearer token", Value: pb.MCPAuthBearer},
	{Name: "oauth (per-user login)", Value: pb.MCPAuthOAuth},
}

// serverKey uniquely identifies a server by owner + name, so two admins can each
// have a server with the same name (e.g. "baki") without colliding.
func serverKey(owner, name string) string { return owner + "/" + name }

// mcpConnection is a live connection to one owner's MCP server plus the tools it
// contributed to the toolbelt (so they can be removed on disconnect).
type mcpConnection struct {
	key        string
	owner      string
	name       string
	url        string
	token      string
	visibility string
	authMode   string
	session    *mcp.ClientSession
	toolNames  []string
}

var (
	mcpConnections   = map[string]*mcpConnection{} // keyed by serverKey
	mcpConnectionsMu sync.Mutex
)

// bearerTransport adds a static bearer token to every request, for MCP servers
// that authenticate over HTTPS with a shared token.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

// InitMCP performs the first sync against PocketBase and starts a background
// reconciler. Servers are managed via the /mcp command, not hardcoded/env-seeded.
func InitMCP(ctx context.Context) {
	syncMCPServers(ctx)

	go func() {
		ticker := time.NewTicker(mcpReconcileInterval)
		defer ticker.Stop()
		for range ticker.C {
			syncMCPServers(context.Background())
		}
	}()
}

// syncMCPServers reconciles live connections with the mcp_servers collection:
// connects newly-enabled servers, disconnects removed/disabled ones, and
// reconnects any whose URL, token, or visibility changed.
func syncMCPServers(ctx context.Context) {
	servers, err := pb.ListMCPServers()
	if err != nil {
		log.Errorf("MCP reconcile: failed to list servers: %v", err)
		return
	}

	desired := map[string]*pb.MCPServer{}
	for _, srv := range servers {
		// OAuth servers are connected per-user via /mcp link, not by the startup
		// reconciler (they have no token until a user authorizes).
		if srv.Enabled && srv.AuthMode != pb.MCPAuthOAuth && strings.TrimSpace(srv.URL) != "" {
			desired[serverKey(srv.Owner, srv.Name)] = srv
		}
	}

	mcpConnectionsMu.Lock()
	current := make(map[string]*mcpConnection, len(mcpConnections))
	for k, v := range mcpConnections {
		current[k] = v
	}
	mcpConnectionsMu.Unlock()

	// Remove connections no longer desired (deleted, disabled, or reconfigured).
	// OAuth connections are managed by /mcp link, not the reconciler, so leave them.
	for key, conn := range current {
		if conn.authMode == pb.MCPAuthOAuth {
			continue
		}
		want, ok := desired[key]
		if !ok || want.URL != conn.url || want.Token != conn.token || want.Visibility != conn.visibility {
			disconnectMCPServer(key)
		}
	}

	// Add connections that are desired but not yet live.
	for key, srv := range desired {
		mcpConnectionsMu.Lock()
		_, live := mcpConnections[key]
		mcpConnectionsMu.Unlock()
		if live {
			continue
		}
		if err := connectMCPServer(ctx, srv); err != nil {
			log.Errorf("MCP reconcile: connect %q (%s) failed: %v", srv.Name, srv.URL, err)
		}
	}
}

// connectMCPServer connects to one server, lists its tools, and registers them
// into the toolbelt tagged with the owner and visibility.
func connectMCPServer(ctx context.Context, srv *pb.MCPServer) error {
	httpClient := &http.Client{Transport: http.DefaultTransport}
	if srv.Token != "" {
		httpClient.Transport = &bearerTransport{token: srv.Token, base: http.DefaultTransport}
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true, // request/response only; no server-initiated stream
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bitbot", Version: "1.0.0"}, nil)

	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return err
	}

	listCtx, cancel2 := context.WithTimeout(ctx, 20*time.Second)
	defer cancel2()
	res, err := session.ListTools(listCtx, nil)
	if err != nil {
		session.Close()
		return err
	}

	registerServerTools(srv, session, res)
	return nil
}

// registerServerTools registers a connected server's tools into the toolbelt
// (tagged with owner and visibility) and stores the live connection. Shared by
// the bearer reconciler path and the OAuth link path.
func registerServerTools(srv *pb.MCPServer, session *mcp.ClientSession, res *mcp.ListToolsResult) {
	key := serverKey(srv.Owner, srv.Name)
	conn := &mcpConnection{key: key, owner: srv.Owner, name: srv.Name, url: srv.URL, token: srv.Token, visibility: srv.Visibility, authMode: srv.AuthMode, session: session}
	for _, tool := range res.Tools {
		destructive := false
		if tool.Annotations != nil && tool.Annotations.DestructiveHint != nil {
			destructive = *tool.Annotations.DestructiveHint
		}
		toolName := tool.Name
		s := session
		registerTool(&registeredTool{
			Name:        toolName,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Source:      key,
			Owner:       srv.Owner,
			Visibility:  srv.Visibility,
			Destructive: destructive,
			Invoke: func(ctx context.Context, userID, channelID, guildID string, args map[string]any) (string, error) {
				return callMCPTool(ctx, s, toolName, args)
			},
		})
		conn.toolNames = append(conn.toolNames, toolName)
	}

	mcpConnectionsMu.Lock()
	mcpConnections[key] = conn
	mcpConnectionsMu.Unlock()

	log.Infof("connected MCP server %q owner=%q (%s): registered %d tools", srv.Name, srv.Owner, srv.URL, len(conn.toolNames))
}

// disconnectMCPServer removes a server's tools from the toolbelt and closes its session.
func disconnectMCPServer(key string) {
	mcpConnectionsMu.Lock()
	conn := mcpConnections[key]
	delete(mcpConnections, key)
	mcpConnectionsMu.Unlock()
	if conn == nil {
		return
	}
	removed := unregisterSource(key)
	if conn.session != nil {
		conn.session.Close()
	}
	log.Infof("disconnected MCP server %q owner=%q: removed %d tools", conn.name, conn.owner, removed)
}

// HandleMCPCommand handles the /mcp slash command (admin only). Each admin owns
// the servers they add; add/remove/access act on the caller's own (or legacy)
// servers, and list shows what the caller can see.
func HandleMCPCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var roles []string
	if i.Member != nil {
		roles = i.Member.Roles
	}
	caller := getUserID(i)
	if !CheckAdmin(caller, roles) {
		respondWithMessage(s, i, "You are not authorized to manage MCP servers.")
		return
	}

	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		respondWithMessage(s, i, "Unknown mcp subcommand.")
		return
	}
	sub := data.Options[0]
	optStr := func(name string) string {
		for _, o := range sub.Options {
			if o.Name == name {
				return o.StringValue()
			}
		}
		return ""
	}

	switch sub.Name {
	case "add":
		name, url, token := optStr("name"), optStr("url"), optStr("token")
		visibility := optStr("visibility") // "" => private
		authMode := optStr("auth_mode")    // "" => bearer
		if name == "" || url == "" {
			respondWithMessage(s, i, "`/mcp add` requires `name` and `url`.")
			return
		}
		created, err := pb.AddMCPServer(name, url, token, caller, visibility, authMode)
		if err != nil {
			respondWithMessage(s, i, "Failed to add MCP server: "+err.Error())
			return
		}
		if !created {
			respondWithMessage(s, i, "You already have a server with that URL.")
			return
		}
		if authMode == pb.MCPAuthOAuth {
			respondWithMessage(s, i, fmt.Sprintf("Added OAuth MCP server `%s`. Run `/mcp link name:%s` to authorize and connect it.", name, name))
			return
		}
		respondWithMessage(s, i, fmt.Sprintf("Added MCP server `%s` (owner: you). Connecting…", name))
		channelID := i.ChannelID
		key := serverKey(caller, name)
		go func() {
			syncMCPServers(context.Background())
			s.ChannelMessageSend(channelID, mcpServerStatusLine(key, name))
		}()

	case "link":
		name := optStr("name")
		if name == "" {
			respondWithMessage(s, i, "`/mcp link` requires `name`.")
			return
		}
		srv, err := pb.GetMCPServer(name, caller)
		if err != nil || srv == nil {
			respondWithMessage(s, i, fmt.Sprintf("No MCP server named `%s` that you can manage.", name))
			return
		}
		if srv.AuthMode != pb.MCPAuthOAuth {
			respondWithMessage(s, i, fmt.Sprintf("`%s` uses bearer auth, not OAuth — nothing to link.", name))
			return
		}
		respondWithMessage(s, i, "Check your DMs for an authorization link…")
		channelID := i.ChannelID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), oauthAuthorizeTimeout+time.Minute)
			defer cancel()
			if err := linkOAuthServer(ctx, s, caller, srv); err != nil {
				s.ChannelMessageSend(channelID, fmt.Sprintf("⚠️ Could not link `%s`: %v", name, err))
				return
			}
			s.ChannelMessageSend(channelID, mcpServerStatusLine(serverKey(srv.Owner, srv.Name), name))
		}()

	case "remove":
		name := optStr("name")
		if name == "" {
			respondWithMessage(s, i, "`/mcp remove` requires `name`.")
			return
		}
		if err := pb.RemoveMCPServer(name, caller); err != nil {
			respondWithMessage(s, i, "Failed to remove MCP server: "+err.Error())
			return
		}
		respondWithMessage(s, i, fmt.Sprintf("Removed MCP server `%s`.", name))
		go syncMCPServers(context.Background())

	case "access":
		name := optStr("name")
		visibility := optStr("visibility")
		if name == "" || visibility == "" {
			respondWithMessage(s, i, "`/mcp access` requires `name` and `visibility`.")
			return
		}
		found, err := pb.SetMCPServerVisibility(name, caller, visibility)
		if err != nil {
			respondWithMessage(s, i, "Failed to update access: "+err.Error())
			return
		}
		if !found {
			respondWithMessage(s, i, fmt.Sprintf("No MCP server named `%s` that you can manage.", name))
			return
		}
		respondWithMessage(s, i, fmt.Sprintf("Set `%s` visibility to **%s**. Reconnecting…", name, visibility))
		go syncMCPServers(context.Background())

	case "list":
		respondWithMessage(s, i, mcpListReport(caller, true))

	default:
		respondWithMessage(s, i, "Unknown mcp subcommand.")
	}
}

// mcpServerStatusLine reports whether a server (by key) is connected and how many
// tools it registered.
func mcpServerStatusLine(key, name string) string {
	mcpConnectionsMu.Lock()
	defer mcpConnectionsMu.Unlock()
	if conn, ok := mcpConnections[key]; ok {
		return fmt.Sprintf("✅ Connected `%s` — registered %d tools.", name, len(conn.toolNames))
	}
	return fmt.Sprintf("⚠️ `%s` was saved but is not connected (check the URL, token, and reachability). It will be retried automatically.", name)
}

// mcpListReport lists the servers the caller can see (their own plus shared ones)
// with live status.
func mcpListReport(caller string, isAdmin bool) string {
	servers, err := pb.ListMCPServers()
	if err != nil {
		return "Failed to list MCP servers: " + err.Error()
	}
	mcpConnectionsMu.Lock()
	defer mcpConnectionsMu.Unlock()

	var sb strings.Builder
	shown := 0
	for _, srv := range servers {
		visible := srv.Owner == caller ||
			srv.Visibility == pb.MCPVisibilityPublic ||
			(srv.Visibility == pb.MCPVisibilityAdmins && isAdmin)
		if !visible {
			continue
		}
		owner := "you"
		if srv.Owner != caller {
			if srv.Owner == "" {
				owner = "system"
			} else {
				owner = srv.Owner
			}
		}
		status := "not connected"
		tools := 0
		if conn, ok := mcpConnections[serverKey(srv.Owner, srv.Name)]; ok {
			status, tools = "connected", len(conn.toolNames)
		} else if !srv.Enabled {
			status = "disabled"
		}
		sb.WriteString(fmt.Sprintf("• `%s` — %s — owner: %s — %s — %s — %d tools\n", srv.Name, srv.URL, owner, srv.Visibility, status, tools))
		shown++
	}
	if shown == 0 {
		return "No MCP servers available to you. Add one with `/mcp add name:<name> url:<url>`."
	}
	return "**MCP servers:**\n" + sb.String()
}

// callMCPTool invokes a remote MCP tool and renders its result as a string for
// the model, preferring the human-readable text content the server returns and
// falling back to its structured JSON payload.
func callMCPTool(ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (string, error) {
	if session == nil {
		return "", fmt.Errorf("MCP server is not connected")
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	if text == "" && res.StructuredContent != nil {
		if b, e := json.Marshal(res.StructuredContent); e == nil {
			text = string(b)
		}
	}
	if text == "" {
		text = "(no output)"
	}

	if res.IsError {
		return jsonResult("error", text), nil
	}
	return jsonResult("success", text), nil
}
