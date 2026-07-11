package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

You also have the capability to manage SSH connections and execute commands on remote servers using the SSH tools provided. To execute commands, you must first connect to a server. You can also generate and show SSH keys. Note that only authorized users (admins) can use SSH tools. If an SSH tool fails due to lack of authorization, politely inform the user.

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
	histMu  sync.Mutex
	turnMu  sync.Mutex
	history []Message
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
	allTools := append(ReminderTools, SSHTools...)

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
		_, err = session.ChannelMessageSend(channelID, reply)
		if err != nil {
			log.Errorf("Error sending message to Discord: %v", err)
		}
		conv.appendAssistant(message)
		return
	}

	// The loop hit maxToolRounds without the model producing a final reply.
	log.Warnf("Tool-handling loop reached max rounds (%d) without a final reply", maxToolRounds)
	_, _ = session.ChannelMessageSend(channelID, "Sorry, I couldn't complete that request. Please try rephrasing or try again later.")
}
