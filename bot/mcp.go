package bot

import (
	"bitbot/pb"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpReconcileInterval is how often the bot re-syncs its live MCP connections
// with the mcp_servers collection, so servers added/removed/toggled in the
// PocketBase admin UI are picked up without a restart.
const mcpReconcileInterval = 60 * time.Second

// mcpConnection is a live connection to one MCP server plus the tools it
// contributed to the toolbelt (so they can be removed on disconnect).
type mcpConnection struct {
	name      string
	url       string
	token     string
	adminOnly bool
	session   *mcp.ClientSession
	toolNames []string
}

var (
	mcpConnections   = map[string]*mcpConnection{} // keyed by server name
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

// InitMCP seeds an MCP server from env (for migration), performs the first sync
// against PocketBase, and starts a background reconciler. Servers are configured
// in the mcp_servers collection; this is a no-op when none are set.
func InitMCP(ctx context.Context) {
	// One-time migration: if BAKI_MCP_URL is set, ensure it exists as a row so the
	// PocketBase collection becomes the single source of truth going forward.
	if url := strings.TrimSpace(os.Getenv("BAKI_MCP_URL")); url != "" {
		if created, err := pb.AddMCPServer("baki", url, strings.TrimSpace(os.Getenv("BAKI_MCP_TOKEN")), true); err != nil {
			log.Warnf("could not seed MCP server from env: %v", err)
		} else if created {
			log.Info("seeded MCP server 'baki' from environment into mcp_servers")
		}
	}

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
// reconnects any whose URL or token changed.
func syncMCPServers(ctx context.Context) {
	servers, err := pb.ListMCPServers()
	if err != nil {
		log.Errorf("MCP reconcile: failed to list servers: %v", err)
		return
	}

	desired := map[string]*pb.MCPServer{}
	for _, s := range servers {
		if s.Enabled && strings.TrimSpace(s.URL) != "" {
			desired[s.Name] = s
		}
	}

	mcpConnectionsMu.Lock()
	current := make(map[string]*mcpConnection, len(mcpConnections))
	for k, v := range mcpConnections {
		current[k] = v
	}
	mcpConnectionsMu.Unlock()

	// Remove connections no longer desired (deleted, disabled, or reconfigured).
	// An admin_only change also triggers a reconnect so tools re-register with the
	// new access level.
	for name, conn := range current {
		want, ok := desired[name]
		if !ok || want.URL != conn.url || want.Token != conn.token || want.AdminOnly != conn.adminOnly {
			disconnectMCPServer(name)
		}
	}

	// Add connections that are desired but not yet live.
	for name, srv := range desired {
		mcpConnectionsMu.Lock()
		_, live := mcpConnections[name]
		mcpConnectionsMu.Unlock()
		if live {
			continue
		}
		if err := connectMCPServer(ctx, srv); err != nil {
			log.Errorf("MCP reconcile: connect %q (%s) failed: %v", name, srv.URL, err)
		}
	}
}

// connectMCPServer connects to one server, lists its tools, and registers them
// into the toolbelt tagged with the server name.
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

	conn := &mcpConnection{name: srv.Name, url: srv.URL, token: srv.Token, adminOnly: srv.AdminOnly, session: session}
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
			Source: srv.Name,
			// Admin requirement is per-server; destructive tools are always gated
			// behind a confirmation button that only an admin can approve.
			AdminOnly:   srv.AdminOnly,
			Destructive: destructive,
			Invoke: func(ctx context.Context, userID, channelID, guildID string, args map[string]any) (string, error) {
				return callMCPTool(ctx, s, toolName, args)
			},
		})
		conn.toolNames = append(conn.toolNames, toolName)
	}

	mcpConnectionsMu.Lock()
	mcpConnections[srv.Name] = conn
	mcpConnectionsMu.Unlock()

	log.Infof("connected MCP server %q (%s): registered %d tools", srv.Name, srv.URL, len(conn.toolNames))
	return nil
}

// disconnectMCPServer removes a server's tools from the toolbelt and closes its session.
func disconnectMCPServer(name string) {
	mcpConnectionsMu.Lock()
	conn := mcpConnections[name]
	delete(mcpConnections, name)
	mcpConnectionsMu.Unlock()
	if conn == nil {
		return
	}
	removed := unregisterSource(name)
	if conn.session != nil {
		conn.session.Close()
	}
	log.Infof("disconnected MCP server %q: removed %d tools", name, removed)
}

// HandleMCPCommand handles the /mcp slash command (admin only), letting servers
// be managed from Discord instead of the PocketBase admin UI. Because connecting
// does network I/O that can exceed Discord's 3s interaction window, add/reload
// acknowledge immediately and post the connection result as a follow-up.
func HandleMCPCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var roles []string
	if i.Member != nil {
		roles = i.Member.Roles
	}
	if !CheckAdmin(getUserID(i), roles) {
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
	// optBool returns the option value, or def if it wasn't provided.
	optBool := func(name string, def bool) bool {
		for _, o := range sub.Options {
			if o.Name == name {
				return o.BoolValue()
			}
		}
		return def
	}

	switch sub.Name {
	case "add":
		name, url, token := optStr("name"), optStr("url"), optStr("token")
		adminOnly := optBool("admin_only", true) // default: admin-only
		if name == "" || url == "" {
			respondWithMessage(s, i, "`/mcp add` requires `name` and `url`.")
			return
		}
		created, err := pb.AddMCPServer(name, url, token, adminOnly)
		if err != nil {
			respondWithMessage(s, i, "Failed to add MCP server: "+err.Error())
			return
		}
		if !created {
			respondWithMessage(s, i, "A server with that URL already exists.")
			return
		}
		respondWithMessage(s, i, fmt.Sprintf("Added MCP server `%s`. Connecting…", name))
		channelID := i.ChannelID
		go func() {
			syncMCPServers(context.Background())
			s.ChannelMessageSend(channelID, mcpServerStatusLine(name))
		}()

	case "access":
		name := optStr("name")
		adminOnly := optBool("admin_only", true)
		if name == "" {
			respondWithMessage(s, i, "`/mcp access` requires `name` and `admin_only`.")
			return
		}
		found, err := pb.SetMCPServerAccess(name, adminOnly)
		if err != nil {
			respondWithMessage(s, i, "Failed to update access: "+err.Error())
			return
		}
		if !found {
			respondWithMessage(s, i, fmt.Sprintf("No MCP server named `%s`.", name))
			return
		}
		access := "public"
		if adminOnly {
			access = "admin-only"
		}
		respondWithMessage(s, i, fmt.Sprintf("Set `%s` to **%s**. Reconnecting…", name, access))
		channelID := i.ChannelID
		go func() {
			syncMCPServers(context.Background()) // detects the admin_only change and reconnects
			s.ChannelMessageSend(channelID, mcpServerStatusLine(name))
		}()

	case "remove":
		name := optStr("name")
		if name == "" {
			respondWithMessage(s, i, "`/mcp remove` requires `name`.")
			return
		}
		if err := pb.RemoveMCPServer(name); err != nil {
			respondWithMessage(s, i, "Failed to remove MCP server: "+err.Error())
			return
		}
		disconnectMCPServer(name)
		respondWithMessage(s, i, fmt.Sprintf("Removed MCP server `%s` and its tools.", name))

	case "reload":
		respondWithMessage(s, i, "Reloading MCP servers…")
		channelID := i.ChannelID
		go func() {
			syncMCPServers(context.Background())
			s.ChannelMessageSend(channelID, mcpListReport())
		}()

	case "list":
		respondWithMessage(s, i, mcpListReport())

	default:
		respondWithMessage(s, i, "Unknown mcp subcommand.")
	}
}

// mcpServerStatusLine reports whether a named server is connected and how many
// tools it registered.
func mcpServerStatusLine(name string) string {
	mcpConnectionsMu.Lock()
	defer mcpConnectionsMu.Unlock()
	if conn, ok := mcpConnections[name]; ok {
		return fmt.Sprintf("✅ Connected `%s` — registered %d tools.", name, len(conn.toolNames))
	}
	return fmt.Sprintf("⚠️ `%s` was saved but is not connected (check the URL, token, and reachability). It will be retried automatically.", name)
}

// mcpListReport lists all configured servers with their live status.
func mcpListReport() string {
	servers, err := pb.ListMCPServers()
	if err != nil {
		return "Failed to list MCP servers: " + err.Error()
	}
	if len(servers) == 0 {
		return "No MCP servers configured. Add one with `/mcp add name:<name> url:<url>`."
	}
	mcpConnectionsMu.Lock()
	defer mcpConnectionsMu.Unlock()
	var sb strings.Builder
	sb.WriteString("**MCP servers:**\n")
	for _, srv := range servers {
		status := "enabled (not connected)"
		tools := 0
		if conn, ok := mcpConnections[srv.Name]; ok {
			status = "connected"
			tools = len(conn.toolNames)
		} else if !srv.Enabled {
			status = "disabled"
		}
		access := "admin-only"
		if !srv.AdminOnly {
			access = "public"
		}
		sb.WriteString(fmt.Sprintf("• `%s` — %s — %s — %s — %d tools\n", srv.Name, srv.URL, status, access, tools))
	}
	return sb.String()
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
