package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
)

// Model name constants
const (
	SystemInstruction = `
Your name is !bit. You are a helpful assistant that can answer any question, have conversations, and assist users with various tasks. You are also able to use tools to assist users with various tasks.

This is a group chat with multiple people. Each user message is prefixed with the speaker's display name and Discord ID in the format "Name [id:123456789]: message". Use these prefixes to tell who is speaking, to answer questions about who said what, and to identify the current user (the speaker of the most recent message is the person you are replying to). Different prefixes mean different people. Never include this prefix in your own replies — reply in natural language as yourself.

You use brief answers by default, but will elaborate or explain when asked to do so.

One of your capabilities is setting reminders for users. When a user asks for a reminder, always convert their time expression to one of the following accepted formats before calling the reminder tool:
- "in 10m", "in 2h", "in 3d" (duration)
- "every 10m", "every 2h", "every 3d" (recurring duration)
- "tomorrow at 8pm", "next monday at 9:30am", "today at 8pm", "at 8pm", "8pm", "20:00" (specific time)
- "every day at 8am", "every monday 8pm" (recurring time)

Do NOT remove spaces between words in time expressions. Always use the exact format, e.g., 'tomorrow at 8pm', not 'tomorrowat8pm'.

If a user requests a reminder for a specific date/time and it is not supported, offer to set a reminder for the equivalent duration instead (e.g., "Would you like me to set a reminder for 'in 24 hours' instead?").

Beyond reminders, you have a toolbelt of extended tools (SSH management, backups, and other integrations) reached through two tools: call "find_tools" to discover what is available (optionally with a query) and read each tool's input schema, then "call_tool" with the exact tool name and an arguments object to run it. Always find_tools before calling an unfamiliar tool so you use the right name and arguments. Some tools are admin-only, and destructive tools require the user to approve a confirmation button before they run — when a destructive call returns a "pending" status, tell the user you have requested confirmation and do not retry. If a tool reports it is not authorized, politely inform the user.

A tool result is returned as JSON with a "status" field ("success" or "error") and a "message" field. If status is "error", immediately reply to the user with the message and do not call the tool again unless the user asks for another attempt.

After calling a tool, always reply to the user in natural language summarizing the result.

If the time has already passed today, set the reminder for tomorrow.`
)

// channelConversation holds the running history for a single channel plus the
// locks that keep concurrent Discord events (each dispatched in its own
// goroutine) from corrupting it.
//
//	histMu guards reads/appends to history so concurrent goroutines never race
//	       on the underlying slice (a plain map/slice race is a fatal runtime
//	       error and would crash the whole bot).
//	turnMu serializes AI reply generation per channel, so when several people
//	       talk to the bot at once their turns are processed one at a time
//	       instead of interleaving into incoherent replies.
type channelConversation struct {
	histMu       sync.Mutex
	turnMu       sync.Mutex
	backfillOnce sync.Once // guards the one-time history backfill from Discord
	history      []Message
}

// backfillCount is how many prior channel messages to pull from Discord to seed
// history after a restart (in-memory history is otherwise empty on boot).
const backfillCount = 30

// maybeBackfill seeds this channel's history once, from the messages that
// precede beforeID in the channel, so that after a restart the bot still has
// context from earlier chat. It runs at most once per conversation (per process)
// and is a no-op on error. Uses the REST API, which returns message content
// regardless of gateway intents.
func (c *channelConversation) maybeBackfill(session *discordgo.Session, channelID, beforeID, botID string) {
	c.backfillOnce.Do(func() {
		msgs, err := session.ChannelMessages(channelID, backfillCount, beforeID, "", "")
		if err != nil {
			log.Warnf("history backfill failed for channel %s: %v", channelID, err)
			return
		}

		// ChannelMessages returns newest-first; walk backwards for chronological order.
		var seed []Message
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if strings.TrimSpace(m.Content) == "" || m.Author == nil {
				continue
			}
			if m.Author.ID == botID {
				seed = append(seed, Message{Role: "assistant", Content: m.Content})
				continue
			}
			name := m.Author.GlobalName
			if name == "" {
				name = m.Author.Username
			}
			seed = append(seed, Message{Role: "user", Content: fmt.Sprintf("%s [id:%s]: %s", name, m.Author.ID, m.Content)})
		}

		c.histMu.Lock()
		// Prepend the fetched context ahead of anything recorded meanwhile.
		c.history = append(seed, c.history...)
		c.history = trimHistory(c.history)
		c.histMu.Unlock()
		log.Infof("backfilled %d prior messages for channel %s", len(seed), channelID)
	})
}

// appendUser records an attributed user message. Used for both messages
// addressed to the bot and passively observed channel chatter, so the model
// has full context on who said what.
func (c *channelConversation) appendUser(userID, displayName, content string) {
	if displayName == "" {
		displayName = "Unknown"
	}
	attributed := fmt.Sprintf("%s [id:%s]: %s", displayName, userID, content)
	c.histMu.Lock()
	defer c.histMu.Unlock()
	c.history = append(c.history, Message{Role: "user", Content: attributed})
	c.history = trimHistory(c.history)
}

// snapshot returns a copy of the current history prefixed with the system
// message, safe to hand to the API without holding the lock during the call.
func (c *channelConversation) snapshot() []Message {
	c.histMu.Lock()
	defer c.histMu.Unlock()
	msgs := make([]Message, 0, len(c.history)+1)
	msgs = append(msgs, Message{Role: "system", Content: SystemInstruction})
	msgs = append(msgs, c.history...)
	return msgs
}

// appendAssistant records the model's messages (assistant replies and the
// assistant/tool message pairs from a tool round) atomically.
func (c *channelConversation) appendAssistant(msgs ...Message) {
	c.histMu.Lock()
	defer c.histMu.Unlock()
	c.history = append(c.history, msgs...)
	c.history = trimHistory(c.history)
}

var (
	// conversations maps channelID -> its conversation state. conversationsMu
	// guards the map itself (creation/lookup); per-channel data is guarded by
	// the locks inside channelConversation.
	conversations   = make(map[string]*channelConversation)
	conversationsMu sync.Mutex

	// maxHistoryMessages caps how many stored messages we keep per channel so the
	// in-memory history (and each request payload) does not grow unbounded.
	maxHistoryMessages = 40

	// Rate limiting
	requestCount         int
	lastRequestTime      time.Time
	requestMutex         sync.Mutex
	rateLimitWindow      = 60 * time.Second // 1 minute window
	maxRequestsPerMinute = 50               // Conservative limit to stay under the 60/minute free tier limit
)

// getConversation returns (creating if needed) the conversation state for a channel.
func getConversation(channelID string) *channelConversation {
	conversationsMu.Lock()
	defer conversationsMu.Unlock()
	c := conversations[channelID]
	if c == nil {
		c = &channelConversation{}
		conversations[channelID] = c
	}
	return c
}

// InitRegoloClient initializes the Regolo (OpenAI-compatible) client, delegating
// to the client in regolo.go while keeping the startup logging/timing.
func InitRegoloClient(apiKey, model string) error {
	startTime := time.Now()
	log.Infof("Starting Regolo client initialization at %v", startTime)

	if err := initRegoloClient(apiKey, model); err != nil {
		log.Errorf("Failed to initialize Regolo client: %v", err)
		return err
	}

	lastRequestTime = time.Now()
	log.Infof("Regolo client initialization completed in %v (model=%s)", time.Since(startTime), regoloModel)
	return nil
}

// checkRateLimit checks if we're within rate limits and returns true if we can proceed
func checkRateLimit() bool {
	requestMutex.Lock()
	defer requestMutex.Unlock()

	now := time.Now()
	if now.Sub(lastRequestTime) > rateLimitWindow {
		// Reset counter if we're in a new window
		requestCount = 0
		lastRequestTime = now
	}

	if requestCount >= maxRequestsPerMinute {
		// Calculate time until next window
		timeToWait := rateLimitWindow - now.Sub(lastRequestTime)
		log.Warnf("Rate limit reached. Please wait %v before trying again.", timeToWait.Round(time.Second))
		return false
	}

	requestCount++
	return true
}

func handleAIError(err error, session *discordgo.Session, channelID string) {
	if err == nil {
		return
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, "RESOURCE_EXHAUSTED") || strings.Contains(errMsg, "429") {
		log.Warn("Rate limit exceeded for AI API")
		_, _ = session.ChannelMessageSend(channelID, "I'm currently experiencing high demand. Please try again in a minute.")
	} else {
		log.Errorf("AI API error: %v", err)
		_, _ = session.ChannelMessageSend(channelID, "Sorry, I encountered an error while processing your request. Please try again later.")
	}
}

// trimHistory caps history to the most recent maxHistoryMessages entries while
// keeping tool-call pairing valid: the OpenAI-compatible API rejects a leading
// role:"tool" message that no longer follows the assistant tool_calls that
// produced it, so drop any orphaned leading tool messages after truncation.
func trimHistory(history []Message) []Message {
	if len(history) > maxHistoryMessages {
		history = history[len(history)-maxHistoryMessages:]
	}
	for len(history) > 0 && history[0].Role == "tool" {
		history = history[1:]
	}
	return history
}

// Discord rejects any bot message whose content exceeds 2000 characters with a
// 400 error, which previously caused long AI replies to silently fail to send.
const (
	discordMessageLimit = 2000
	// safeChunkLimit leaves headroom for code-fence markers that balanceCodeFences
	// may add, so a balanced chunk never exceeds discordMessageLimit.
	safeChunkLimit = discordMessageLimit - 16
	// maxReplyChunks caps how many messages a single reply may span so a runaway
	// response can't spam the channel; anything beyond is truncated.
	maxReplyChunks = 8
)

// messageSendDelay paces the messages of a multi-part reply so they don't spam
// the channel in a single burst.
const messageSendDelay = 1500 * time.Millisecond

// runeIndex returns the byte offset just after the n-th rune (or len(s)).
func runeIndex(s string, n int) int {
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}

// splitForDiscord breaks content into chunks that each fit within limit
// characters, preferring paragraph, then line, then word boundaries, and only
// hard-cutting when a single token is longer than the limit. Splits happen on
// rune boundaries so multibyte characters are never cut in half.
func splitForDiscord(content string, limit int) []string {
	content = strings.TrimRight(content, "\n")
	if utf8.RuneCountInString(content) <= limit {
		return []string{content}
	}

	var chunks []string
	remaining := content
	for utf8.RuneCountInString(remaining) > limit {
		cut := runeIndex(remaining, limit)
		window := remaining[:cut]

		split := -1
		for _, sep := range []string{"\n\n", "\n", " "} {
			if idx := strings.LastIndex(window, sep); idx > 0 {
				split = idx + len(sep)
				break
			}
		}
		if split <= 0 {
			split = cut // no usable boundary: hard cut on the rune boundary
		}

		chunk := strings.TrimRight(remaining[:split], " \n")
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		remaining = strings.TrimLeft(remaining[split:], " \n")
	}
	if strings.TrimSpace(remaining) != "" {
		chunks = append(chunks, remaining)
	}
	return chunks
}

// markdownState captures formatting left open at a chunk boundary so it can be
// reopened in the next chunk.
type markdownState struct {
	fence string // "```go" etc. if a fenced code block is open, else ""
	code  bool   // inside an inline `code` span
	bold  bool   // inside a **bold** span
}

// scanMarkdown returns the formatting still open at the end of s, given the
// state it started in. Markers inside a fenced code block are ignored, and
// inline code is treated as single-line (Discord does not render it across
// newlines).
func scanMarkdown(s string, st markdownState) markdownState {
	lines := strings.Split(s, "\n")
	for idx, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if st.fence != "" {
				st.fence = ""
			} else {
				st.fence = strings.TrimSpace(line)
			}
			continue
		}
		if st.fence != "" {
			continue
		}
		for i := 0; i < len(line); i++ {
			switch {
			case line[i] == '`':
				st.code = !st.code
			case !st.code && i+1 < len(line) && line[i] == '*' && line[i+1] == '*':
				st.bold = !st.bold
				i++
			}
		}
		// Inline code does not render across a real newline, so reset it at each
		// line boundary — but not after a final segment that has no trailing
		// newline (a mid-line hard split), where the span continues into the next
		// chunk and must be carried.
		if idx < len(lines)-1 {
			st.code = false
		}
	}
	return st
}

// balanceMarkdown keeps code fences (```), bold (**) and inline code (`) from
// being left unclosed when a reply is split across messages: whatever is open at
// the end of a chunk is closed there and reopened at the start of the next. For
// well-formed markdown split on line boundaries this is a no-op; it only kicks in
// when a fenced block spans chunks or a rare mid-line hard split cuts a span.
func balanceMarkdown(chunks []string) []string {
	out := make([]string, 0, len(chunks))
	var carry markdownState
	for _, c := range chunks {
		// Reopen whatever the previous chunk left open.
		if carry.fence != "" {
			c = carry.fence + "\n" + c
		} else {
			if carry.bold {
				c = "**" + c
			}
			if carry.code {
				c = "`" + c
			}
		}

		st := scanMarkdown(c, markdownState{})

		// Close whatever this chunk leaves open.
		if st.fence != "" {
			c += "\n```"
		} else {
			if st.code {
				c += "`"
			}
			if st.bold {
				c += "**"
			}
		}

		carry = st
		out = append(out, c)
	}
	return out
}

// truncateToLimit trims s to at most limit runes.
func truncateToLimit(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// sendReply sends an AI reply to Discord, splitting it into multiple sequential
// messages when it exceeds Discord's per-message character limit. discordgo's
// built-in rate limiter paces the sends, so this won't trip Discord's rate
// limits; maxReplyChunks additionally guards against flooding the channel.
func sendReply(session *discordgo.Session, channelID, content string) {
	chunks := balanceMarkdown(splitForDiscord(content, safeChunkLimit))

	if len(chunks) > maxReplyChunks {
		chunks = chunks[:maxReplyChunks]
		notice := "\n… (response truncated)"
		last := chunks[maxReplyChunks-1]
		chunks[maxReplyChunks-1] = truncateToLimit(last, discordMessageLimit-utf8.RuneCountInString(notice)) + notice
	}

	for i, ch := range chunks {
		if strings.TrimSpace(ch) == "" {
			continue
		}
		// Pace multi-message replies so they read as a natural sequence rather
		// than a burst (and give Discord's rate limiter room to breathe).
		if i > 0 {
			session.ChannelTyping(channelID)
			time.Sleep(messageSendDelay)
		}
		if _, err := session.ChannelMessageSend(channelID, ch); err != nil {
			log.Errorf("Error sending message chunk to Discord: %v", err)
			return // stop on error rather than hammering the API
		}
	}
}

// recordMessage stores an attributed user message in the channel's history
// without generating a reply. Used for passive listening so the bot has context
// on messages that were not addressed to it.
func recordMessage(channelID, userID, displayName, content string) {
	if content == "" {
		return
	}
	getConversation(channelID).appendUser(userID, displayName, content)
}

// chatbot generates and sends the bot's reply for a channel. The triggering
// user message must already have been recorded via recordMessage. The whole
// turn is serialized per channel (turnMu) so simultaneous requests from
// different users don't interleave.
func chatbot(session *discordgo.Session, userID string, channelID string, guildID string) {
	if regoloAPIKey == "" {
		log.Error("Regolo client is not initialized.")
		_, _ = session.ChannelMessageSend(channelID, "Sorry, the chat service is not properly configured.")
		return
	}

	if !checkRateLimit() {
		_, _ = session.ChannelMessageSend(channelID, "I'm currently experiencing high demand. Please try again in a minute.")
		return
	}

	ctx := context.Background()
	conv := getConversation(channelID)

	// Only one AI turn per channel at a time. Other users' triggers wait here;
	// their messages are already in history via recordMessage, so this turn sees
	// them and later turns see this turn's exchange.
	conv.turnMu.Lock()
	defer conv.turnMu.Unlock()

	_ = session.ChannelTyping(channelID)

	// Combine tools
	// Reminders stay as direct top-level tools; everything else (SSH, remote MCP
	// tools) is reached through the toolbelt so the per-request tool list stays small.
	allTools := append(append([]Tool{}, ReminderTools...), ToolbeltTools...)

	// Robust function call handling loop with a bounded number of tool rounds so
	// a model that keeps emitting tool_calls cannot spin forever (unbounded API
	// calls, permanent 'typing' state, runaway cost).
	const maxToolRounds = 6
	for i := 0; i < maxToolRounds; i++ {
		// Respect the rate window on every round, not just at entry, so a single
		// user message cannot fire many API calls without a cap.
		if i > 0 && !checkRateLimit() {
			_, _ = session.ChannelMessageSend(channelID, "I'm currently experiencing high demand. Please try again in a minute.")
			return
		}

		messages := conv.snapshot()

		resp, err := RegoloChat(ctx, messages, allTools)
		if err != nil {
			log.Errorf("Error getting response from AI: %v", err)
			handleAIError(err, session, channelID)
			return
		}

		message := resp.Choices[0].Message

		if len(message.ToolCalls) > 0 {
			// Append the assistant message and its tool results together so the
			// tool_calls/tool pairing stays contiguous in history.
			toolMsgs := []Message{message}

			for _, tc := range message.ToolCalls {
				log.Infof("Handling function call: %s", tc.Function.Name)
				result, err := HandleFunctionCallWithContext(session, nil, &tc, userID, channelID, guildID)
				if err != nil {
					log.Errorf("Error handling function call: %v", err)
					conv.appendAssistant(toolMsgs...)
					handleAIError(err, session, channelID)
					return
				}
				log.Infof("Function call '%s' result: %s", tc.Function.Name, result)

				toolMsgs = append(toolMsgs, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    result,
				})
			}
			conv.appendAssistant(toolMsgs...)
			// Loop again so the model can turn the tool results into a reply.
			continue
		}

		// No tool calls: send the assistant reply to Discord. Guard against an
		// empty/whitespace-only body (e.g. finish_reason length/content_filter),
		// which Discord's API rejects, leaving the user with no reply.
		reply := message.Content
		if strings.TrimSpace(reply) == "" {
			reply = "Sorry, I couldn't generate a response. Please try again."
		}
		sendReply(session, channelID, reply)
		conv.appendAssistant(message)
		return
	}

	// The loop hit maxToolRounds without the model producing a final reply.
	log.Warnf("Tool-handling loop reached max rounds (%d) without a final reply", maxToolRounds)
	_, _ = session.ChannelMessageSend(channelID, "Sorry, I couldn't complete that request. Please try rephrasing or try again later.")
}
