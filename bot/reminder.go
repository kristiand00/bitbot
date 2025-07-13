package bot

import (
	"bitbot/pb"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/charmbracelet/log"
)

// handleRemindCommand delegates processing for /remind subcommands
func HandleRemindCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	subCommand := i.ApplicationCommandData().Options[0].Name
	switch subCommand {
	case "add":
		handleAddReminder(s, i)
	case "list":
		handleListReminders(s, i)
	case "delete":
		handleDeleteReminder(s, i)
	default:
		respondWithMessage(s, i, "Unknown remind subcommand.")
	}
}

// --- Reminder Logic ---

// handleAddReminder processes the /remind add command.
func handleAddReminder(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options[0].Options // Options for the "add" subcommand

	var whoArg, whenArg, messageArg string
	for _, opt := range options {
		switch opt.Name {
		case "who":
			whoArg = opt.StringValue()
		case "when":
			whenArg = opt.StringValue()
		case "message":
			messageArg = opt.StringValue()
		}
	}

	if whoArg == "" || whenArg == "" || messageArg == "" {
		respondWithMessage(s, i, "Missing required arguments for adding a reminder.")
		return
	}

	// 1. Parse 'who' argument
	var targetUserIDs []string
	rawTargetUserIDs := strings.Split(whoArg, ",")
	for _, idStr := range rawTargetUserIDs {
		trimmedID := strings.TrimSpace(idStr)
		if trimmedID == "@me" {
			targetUserIDs = append(targetUserIDs, getUserID(i))
		} else {
			// Basic validation: check if it's a user mention or a raw ID
			re := regexp.MustCompile(`<@!?(\d+)>`)
			matches := re.FindStringSubmatch(trimmedID)
			if len(matches) == 2 {
				targetUserIDs = append(targetUserIDs, matches[1])
			} else if _, err := strconv.ParseUint(trimmedID, 10, 64); err == nil {
				// Looks like a raw ID
				targetUserIDs = append(targetUserIDs, trimmedID)
			} else {
				respondWithMessage(s, i, fmt.Sprintf("Invalid user format: '%s'. Please use @mention, user ID, or '@me'.", trimmedID))
				return
			}
		}
	}
	if len(targetUserIDs) == 0 {
		respondWithMessage(s, i, "No valid target users specified.")
		return
	}
	// Remove duplicates
	seen := make(map[string]bool)
	uniqueTargetUserIDs := []string{}
	for _, id := range targetUserIDs {
		if !seen[id] {
			seen[id] = true
			uniqueTargetUserIDs = append(uniqueTargetUserIDs, id)
		}
	}
	targetUserIDs = uniqueTargetUserIDs

	// 2. Parse 'when' argument
	reminderTime, isRecurring, recurrenceRule, err := parseWhenSimple(whenArg)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("Error parsing 'when' argument: %v. Supported formats: 'in Xm/Xh/Xd'", err))
		return
	}

	// 3. Create Reminder struct
	reminderTime = reminderTime.In(reminderLocation)
	var nextReminderTime time.Time
	if isRecurring {
		nextReminderTime = reminderTime
	}
	reminder := &pb.Reminder{
		UserID:           getUserID(i),
		TargetUserIDs:    targetUserIDs,
		Message:          messageArg,
		ChannelID:        i.ChannelID,
		GuildID:          i.GuildID, // Will be empty for DMs, which is fine
		ReminderTime:     reminderTime,
		IsRecurring:      isRecurring,
		RecurrenceRule:   recurrenceRule,
		NextReminderTime: nextReminderTime,
	}

	log.Infof("handleAddReminder: about to save reminder with targetUserIDs: %v", targetUserIDs)

	// 4. Save to PocketBase
	err = pb.CreateReminder(reminder)
	if err != nil {
		log.Errorf("Failed to create reminder: %v", err)
		respondWithMessage(s, i, "Sorry, I couldn't save your reminder. Please try again later.")
		return
	}

	// 5. Confirm to user
	var targetUsersString []string
	for _, uid := range targetUserIDs {
		targetUsersString = append(targetUsersString, fmt.Sprintf("<@%s>", uid))
	}

	timeFormat := "Jan 2, 2006 at 15:04 (Europe/Zagreb)"
	confirmationMsg := fmt.Sprintf("Okay, I'll remind %s on %s about: \"%s\"",
		strings.Join(targetUsersString, ", "),
		reminderTime.Format(timeFormat),
		messageArg)
	if isRecurring {
		confirmationMsg += fmt.Sprintf(" (recurs %s)", recurrenceRule)
	}

	respondWithMessage(s, i, confirmationMsg)
}

// handleListReminders lists all reminders for the user.
func handleListReminders(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := getUserID(i)
	reminders, err := pb.ListRemindersByUser(userID)
	if err != nil {
		log.Errorf("Failed to list reminders for user %s: %v", userID, err)
		respondWithMessage(s, i, "Could not fetch your reminders. Please try again later.")
		return
	}

	if len(reminders) == 0 {
		respondWithMessage(s, i, "You have no active reminders.")
		return
	}

	timeFormat := "Jan 2, 2006 at 3:04 PM (Europe/Zagreb)"
	var contentBuilder strings.Builder
	var components []discordgo.MessageComponent
	var deleteButtons []discordgo.MessageComponent

	contentBuilder.WriteString("**Your active reminders:**\n\n")
	for idx, r := range reminders {
		var nextDue time.Time
		if r.IsRecurring {
			nextDue = r.NextReminderTime
		} else {
			nextDue = r.ReminderTime
		}

		var nextDueStr string
		if !nextDue.IsZero() {
			nextDueStr = nextDue.In(reminderLocation).Format(timeFormat)
		} else {
			nextDueStr = "N/A (Error in time)"
		}

		var targets []string
		for _, tUID := range r.TargetUserIDs {
			targets = append(targets, fmt.Sprintf("<@%s>", tUID))
		}
		targetStr := strings.Join(targets, ", ")

		contentBuilder.WriteString(fmt.Sprintf("%d. To: %s\nMessage: %s\nNext Due: %s\n", idx+1, targetStr, r.Message, nextDueStr))
		if r.IsRecurring {
			contentBuilder.WriteString(fmt.Sprintf("Recurs: %s\n", r.RecurrenceRule))
		}
		contentBuilder.WriteString("\n")

		deleteButtons = append(deleteButtons, &discordgo.Button{
			Label:    fmt.Sprintf("Delete %d", idx+1),
			CustomID: fmt.Sprintf("reminder_delete_%s", r.ID),
			Style:    discordgo.DangerButton,
		})
	}

	// Group delete buttons into rows of up to 5
	for i := 0; i < len(deleteButtons); i += 5 {
		end := i + 5
		if end > len(deleteButtons) {
			end = len(deleteButtons)
		}
		components = append(components, &discordgo.ActionsRow{
			Components: deleteButtons[i:end],
		})
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    contentBuilder.String(),
			Flags:      discordgo.MessageFlagsEphemeral,
			Components: components,
		},
	})
}

// handleDeleteReminder deletes a reminder by ID.
func handleDeleteReminder(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := getUserID(i)
	reminderID := ""
	for _, opt := range i.ApplicationCommandData().Options[0].Options {
		if opt.Name == "id" {
			reminderID = opt.StringValue()
		}
	}
	if reminderID == "" {
		respondWithMessage(s, i, "Missing reminder ID to delete.")
		return
	}
	reminder, err := pb.GetReminderByID(reminderID)
	if err != nil {
		respondWithMessage(s, i, "Could not find the reminder to delete.")
		return
	}
	if reminder.UserID != userID {
		respondWithMessage(s, i, "You can only delete reminders you created.")
		return
	}
	err = pb.DeleteReminder(reminderID)
	if err != nil {
		respondWithMessage(s, i, "Failed to delete the reminder. Please try again.")
		return
	}
	respondWithMessage(s, i, "Reminder deleted successfully.")
}

// --- Context-free Reminder Logic for Gemini and Discord ---

// AddReminderCore adds a reminder for a user, given user/channel context and arguments.
func AddReminderCore(userID, channelID, whoArg, whenArg, messageArg string) (string, error) {
	if whoArg == "" || whenArg == "" || messageArg == "" {
		return "Missing required arguments for adding a reminder.", fmt.Errorf("missing arguments")
	}

	// 1. Parse 'who' argument
	var targetUserIDs []string
	rawTargetUserIDs := strings.Split(whoArg, ",")
	for _, idStr := range rawTargetUserIDs {
		trimmedID := strings.TrimSpace(idStr)
		if trimmedID == "@me" {
			targetUserIDs = append(targetUserIDs, userID)
		} else {
			re := regexp.MustCompile(`<@!?(\d+)>`)
			matches := re.FindStringSubmatch(trimmedID)
			if len(matches) == 2 {
				targetUserIDs = append(targetUserIDs, matches[1])
			} else if _, err := strconv.ParseUint(trimmedID, 10, 64); err == nil {
				targetUserIDs = append(targetUserIDs, trimmedID)
			} else {
				return fmt.Sprintf("Invalid user format: '%s'. Please use @mention, user ID, or '@me'.", trimmedID), fmt.Errorf("invalid user format")
			}
		}
	}
	if len(targetUserIDs) == 0 {
		return "No valid target users specified.", fmt.Errorf("no valid target users")
	}
	// Remove duplicates
	seen := make(map[string]bool)
	uniqueTargetUserIDs := []string{}
	for _, id := range targetUserIDs {
		if !seen[id] {
			seen[id] = true
			uniqueTargetUserIDs = append(uniqueTargetUserIDs, id)
		}
	}
	targetUserIDs = uniqueTargetUserIDs

	// 2. Parse 'when' argument
	reminderTime, isRecurring, recurrenceRule, err := parseWhenSimple(whenArg)
	if err != nil {
		return fmt.Sprintf("Error parsing 'when' argument: %v. Supported formats: 'in Xm/Xh/Xd'", err), err
	}

	// 3. Create Reminder struct
	reminderTime = reminderTime.In(reminderLocation)
	var nextReminderTime time.Time
	if isRecurring {
		nextReminderTime = reminderTime
	}
	reminder := &pb.Reminder{
		UserID:           userID,
		TargetUserIDs:    targetUserIDs,
		Message:          messageArg,
		ChannelID:        channelID,
		GuildID:          "", // Not used for Gemini
		ReminderTime:     reminderTime,
		IsRecurring:      isRecurring,
		RecurrenceRule:   recurrenceRule,
		NextReminderTime: nextReminderTime,
	}

	// 4. Save to PocketBase
	err = pb.CreateReminder(reminder)
	if err != nil {
		return "Sorry, I couldn't save your reminder. Please try again later.", err
	}

	// 5. Confirm to user
	var targetUsersString []string
	for _, uid := range targetUserIDs {
		targetUsersString = append(targetUsersString, fmt.Sprintf("<@%s>", uid))
	}

	timeFormat := "Jan 2, 2006 at 15:04 (Europe/Zagreb)"
	confirmationMsg := fmt.Sprintf("Okay, I'll remind %s on %s about: \"%s\"",
		strings.Join(targetUsersString, ", "),
		reminderTime.Format(timeFormat),
		messageArg)
	if isRecurring {
		confirmationMsg += fmt.Sprintf(" (recurs %s)", recurrenceRule)
	}
	return confirmationMsg, nil
}

// ListRemindersCore lists all reminders for a user.
func ListRemindersCore(userID string) (string, error) {
	reminders, err := pb.ListRemindersByUser(userID)
	if err != nil {
		return "Could not fetch your reminders. Please try again later.", err
	}
	if len(reminders) == 0 {
		return "You have no active reminders.", nil
	}

	timeFormat := "Jan 2, 2006 at 3:04 PM (Europe/Zagreb)"
	var contentBuilder strings.Builder
	contentBuilder.WriteString("Your active reminders:\n\n")
	for idx, r := range reminders {
		var nextDue time.Time
		if r.IsRecurring {
			nextDue = r.NextReminderTime
		} else {
			nextDue = r.ReminderTime
		}
		var nextDueStr string
		if !nextDue.IsZero() {
			nextDueStr = nextDue.In(reminderLocation).Format(timeFormat)
		} else {
			nextDueStr = "N/A (Error in time)"
		}
		var targets []string
		for _, tUID := range r.TargetUserIDs {
			targets = append(targets, fmt.Sprintf("<@%s>", tUID))
		}
		targetStr := strings.Join(targets, ", ")
		contentBuilder.WriteString(fmt.Sprintf("%d. To: %s\nMessage: %s\nNext Due: %s\nID: %s\n", idx+1, targetStr, r.Message, nextDueStr, r.ID))
		if r.IsRecurring {
			contentBuilder.WriteString(fmt.Sprintf("Recurs: %s\n", r.RecurrenceRule))
		}
		contentBuilder.WriteString("\n")
	}
	return contentBuilder.String(), nil
}

// DeleteReminderCore deletes a reminder by ID for a user.
func DeleteReminderCore(userID, reminderID string) (string, error) {
	if reminderID == "" {
		return "Missing reminder ID to delete.", fmt.Errorf("missing reminder ID")
	}
	reminder, err := pb.GetReminderByID(reminderID)
	if err != nil {
		return "Could not find the reminder to delete.", err
	}
	if reminder.UserID != userID {
		return "You can only delete reminders you created.", fmt.Errorf("not allowed")
	}
	err = pb.DeleteReminder(reminderID)
	if err != nil {
		return "Failed to delete the reminder. Please try again.", err
	}
	return "Reminder deleted successfully.", nil
}

// parseWhenSimple is a basic parser for "in Xm/Xh/Xd" and other supported formats.
func parseWhenSimple(whenStr string) (time.Time, bool, string, error) {
	whenStr = strings.ToLower(strings.TrimSpace(whenStr))
	log.Infof("parseWhenSimple: original input: '%s'", whenStr)
	// Normalize natural language variants to compact format (but be careful with day names)
	replacements := []struct{ from, to string }{
		{"minutes", "m"},
		{"minute", "m"},
		{"hours", "h"},
		{"hour", "h"},
	}
	for _, r := range replacements {
		whenStr = strings.ReplaceAll(whenStr, r.from, r.to)
	}

	// Handle "days" and "day" more carefully to avoid affecting day names
	whenStr = regexp.MustCompile(`\b(\d+)\s*days?\b`).ReplaceAllString(whenStr, "$1d")
	// --- NEW: Normalize missing spaces in common expressions ---
	whenStr = regexp.MustCompile(`(tomorrow|today|next[a-z]+)at`).ReplaceAllString(whenStr, "$1 at ")
	whenStr = regexp.MustCompile(`everydayat`).ReplaceAllString(whenStr, "every day at ")
	whenStr = regexp.MustCompile(`every([a-z]+)at`).ReplaceAllString(whenStr, "every $1 at ")

	// Also handle e.g. 'in 10 m' -> 'in 10m' (but only for duration formats)
	if strings.HasPrefix(whenStr, "in ") || regexp.MustCompile(`^\d+[mhd]$`).MatchString(whenStr) {
		whenStr = strings.ReplaceAll(whenStr, " ", "")
	}

	// Handle malformed day abbreviations (e.g., "sun8pm" -> "every sunday 8pm")
	whenStr = regexp.MustCompile(`^(sun|mon|tue|wed|thu|fri|sat)([0-9:]+[ap]m)$`).ReplaceAllString(whenStr, "every $1day $2")
	whenStr = regexp.MustCompile(`^(sun|mon|tue|wed|thu|fri|sat)([0-9:]+)$`).ReplaceAllString(whenStr, "every $1day $2")

	// Handle truncated day names (e.g., "sund8pm" -> "every sunday 8pm")
	whenStr = regexp.MustCompile(`^(sund|mond|tued|wedd|thud|frid|satd)([0-9:]+[ap]m)$`).ReplaceAllString(whenStr, "every ${1}ay $2")
	whenStr = regexp.MustCompile(`^(sund|mond|tued|wedd|thud|frid|satd)([0-9:]+)$`).ReplaceAllString(whenStr, "every ${1}ay $2")

	log.Infof("parseWhenSimple: after normalization: '%s'", whenStr)

	now := time.Now().In(reminderLocation)
	isRecurring := false
	recurrenceRule := ""

	// Check for 'every' keyword for recurrence
	if strings.HasPrefix(whenStr, "every") {
		isRecurring = true
		whenStr = strings.TrimPrefix(whenStr, "every")
	}

	// Regex for 'in Xunit' or 'Xunit'
	re := regexp.MustCompile(`^(?:in)?(\d+)([mhd])$`)
	matches := re.FindStringSubmatch(whenStr)

	if len(matches) == 3 {
		value, err := strconv.Atoi(matches[1])
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid number: %s", matches[1])
		}
		unit := matches[2]
		duration := time.Duration(value)

		switch unit {
		case "m":
			duration *= time.Minute
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d minutes", value)
			}
		case "h":
			duration *= time.Hour
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d hours", value)
			}
		case "d":
			duration *= time.Hour * 24
			if isRecurring {
				recurrenceRule = fmt.Sprintf("every %d days", value)
			}
		default:
			return time.Time{}, false, "", fmt.Errorf("unknown time unit: %s", unit)
		}

		if isRecurring && recurrenceRule == "" {
			return time.Time{}, false, "", fmt.Errorf("could not determine recurrence rule for: %s", whenStr)
		}

		return now.Add(duration), isRecurring, recurrenceRule, nil
	}

	// --- NEW: Support for 'tomorrow at 8pm', 'today at 8pm', 'next monday at 9:30am' ---
	// tomorrow at X
	if strings.HasPrefix(whenStr, "tomorrow at ") {
		timePart := strings.TrimPrefix(whenStr, "tomorrow at ")
		t, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
		}
		tomorrow := now.AddDate(0, 0, 1)
		reminderTime := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
		return reminderTime, false, "", nil
	}
	// today at X
	if strings.HasPrefix(whenStr, "today at ") {
		timePart := strings.TrimPrefix(whenStr, "today at ")
		t, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
		}
		reminderTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
		// If the time has already passed today, set for tomorrow
		if !reminderTime.After(now) {
			reminderTime = reminderTime.AddDate(0, 0, 1)
		}
		return reminderTime, false, "", nil
	}
	// next <weekday> at X
	nextDayRe := regexp.MustCompile(`^next([a-z]+) at (.+)$`)
	if m := nextDayRe.FindStringSubmatch(whenStr); len(m) == 3 {
		weekdayStr := m[1]
		timePart := m[2]
		baseDay, err := parseNextDay(weekdayStr)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid weekday: %v", err)
		}
		t, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
		}
		reminderTime := time.Date(baseDay.Year(), baseDay.Month(), baseDay.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
		return reminderTime, false, "", nil
	}
	// at X (today or tomorrow)
	if strings.HasPrefix(whenStr, "at ") {
		timePart := strings.TrimPrefix(whenStr, "at ")
		t, err := parseTimeOfDay(timePart)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
		}
		reminderTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
		if !reminderTime.After(now) {
			reminderTime = reminderTime.AddDate(0, 0, 1)
		}
		return reminderTime, false, "", nil
	}
	// Xpm/Xam/HH:MM (today or tomorrow)
	t, err := parseTimeOfDay(whenStr)
	if err == nil {
		reminderTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
		if !reminderTime.After(now) {
			reminderTime = reminderTime.AddDate(0, 0, 1)
		}
		return reminderTime, false, "", nil
	}

	// --- NEW: Support for recurring time formats like "every sunday 8pm" ---
	if isRecurring {
		// every <weekday> <time>
		recurringDayRe := regexp.MustCompile(`^every ([a-z]+) ([0-9:]+[ap]m|[0-9:]+)$`)
		if m := recurringDayRe.FindStringSubmatch(whenStr); len(m) == 3 {
			weekdayStr := m[1]
			timePart := m[2]

			// Parse the weekday
			dayMap := map[string]time.Weekday{
				"monday":    time.Monday,
				"mon":       time.Monday,
				"tuesday":   time.Tuesday,
				"tue":       time.Tuesday,
				"wednesday": time.Wednesday,
				"wed":       time.Wednesday,
				"thursday":  time.Thursday,
				"thu":       time.Thursday,
				"friday":    time.Friday,
				"fri":       time.Friday,
				"saturday":  time.Saturday,
				"sat":       time.Saturday,
				"sunday":    time.Sunday,
				"sun":       time.Sunday,
			}
			targetDay, exists := dayMap[weekdayStr]
			if !exists {
				return time.Time{}, false, "", fmt.Errorf("unknown weekday: %s", weekdayStr)
			}

			// Parse the time
			t, err := parseTimeOfDay(timePart)
			if err != nil {
				return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
			}

			// Calculate next occurrence
			currentDay := now.Weekday()
			daysUntil := int(targetDay - currentDay)
			if daysUntil <= 0 {
				daysUntil += 7 // Move to next week
			}
			nextOccurrence := now.AddDate(0, 0, daysUntil)
			reminderTime := time.Date(nextOccurrence.Year(), nextOccurrence.Month(), nextOccurrence.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)

			recurrenceRule := fmt.Sprintf("every %s", weekdayStr)
			return reminderTime, true, recurrenceRule, nil
		}

		// every day at <time>
		if strings.HasPrefix(whenStr, "every day at ") {
			timePart := strings.TrimPrefix(whenStr, "every day at ")
			t, err := parseTimeOfDay(timePart)
			if err != nil {
				return time.Time{}, false, "", fmt.Errorf("invalid time of day: %v", err)
			}

			reminderTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, reminderLocation)
			if !reminderTime.After(now) {
				reminderTime = reminderTime.AddDate(0, 0, 1)
			}

			return reminderTime, true, "every day", nil
		}

		return time.Time{}, false, "", fmt.Errorf("unsupported recurring format: '%s'. Try 'every Xm/Xh/Xd', 'every day at Xam/pm', or 'every monday 8pm'. Tip: You can also use 'every day at 8am', 'every monday 8pm', etc.", whenStr)
	}
	return time.Time{}, false, "", fmt.Errorf("unsupported time format: '%s'. Try 'in Xm/Xh/Xd', 'tomorrow at Xam/pm', 'next [day] at Xam/pm', 'today at 8pm', 'at 8pm', or just '8pm'. Tip: If the time has already passed today, the reminder will be set for tomorrow.", whenStr)
}

// parseTimeOfDay parses time strings like "10am", "3:30pm", "14:30"
func parseTimeOfDay(timeStr string) (time.Time, error) {
	timeStr = strings.ToLower(strings.TrimSpace(timeStr))

	// Handle 12-hour format with am/pm
	if strings.Contains(timeStr, "am") || strings.Contains(timeStr, "pm") {
		isPM := strings.Contains(timeStr, "pm")
		timeStr = strings.ReplaceAll(timeStr, "am", "")
		timeStr = strings.ReplaceAll(timeStr, "pm", "")
		timeStr = strings.TrimSpace(timeStr)
		var hour, minute int
		if strings.Contains(timeStr, ":") {
			parts := strings.Split(timeStr, ":")
			if len(parts) != 2 {
				return time.Time{}, fmt.Errorf("invalid time format: %s", timeStr)
			}
			var err error
			hour, err = strconv.Atoi(parts[0])
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid hour: %s", parts[0])
			}
			minute, err = strconv.Atoi(parts[1])
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid minute: %s", parts[1])
			}
		} else {
			var err error
			hour, err = strconv.Atoi(timeStr)
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid hour: %s", timeStr)
			}
			minute = 0
		}
		if isPM && hour != 12 {
			hour += 12
		} else if !isPM && hour == 12 {
			hour = 0
		}
		if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return time.Time{}, fmt.Errorf("time out of range: %02d:%02d", hour, minute)
		}
		return time.Date(2000, 1, 1, hour, minute, 0, 0, reminderLocation), nil
	}
	// Handle 24-hour format
	if strings.Contains(timeStr, ":") {
		parts := strings.Split(timeStr, ":")
		if len(parts) != 2 {
			return time.Time{}, fmt.Errorf("invalid time format: %s", timeStr)
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid hour: %s", parts[0])
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid minute: %s", parts[1])
		}
		if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return time.Time{}, fmt.Errorf("time out of range: %02d:%02d", hour, minute)
		}
		return time.Date(2000, 1, 1, hour, minute, 0, 0, reminderLocation), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format: %s", timeStr)
}

// parseNextDay parses day strings like "monday", "tuesday", etc.
func parseNextDay(dayStr string) (time.Time, error) {
	dayStr = strings.ToLower(strings.TrimSpace(dayStr))
	dayMap := map[string]time.Weekday{
		"monday":    time.Monday,
		"mon":       time.Monday,
		"tuesday":   time.Tuesday,
		"tue":       time.Tuesday,
		"wednesday": time.Wednesday,
		"wed":       time.Wednesday,
		"thursday":  time.Thursday,
		"thu":       time.Thursday,
		"friday":    time.Friday,
		"fri":       time.Friday,
		"saturday":  time.Saturday,
		"sat":       time.Saturday,
		"sunday":    time.Sunday,
		"sun":       time.Sunday,
	}
	targetDay, exists := dayMap[dayStr]
	if !exists {
		return time.Time{}, fmt.Errorf("unknown day: %s", dayStr)
	}
	now := time.Now().In(reminderLocation)
	currentDay := now.Weekday()
	daysUntil := int(targetDay - currentDay)
	if daysUntil <= 0 {
		daysUntil += 7 // Move to next week
	}
	return now.AddDate(0, 0, daysUntil), nil
}

// StartReminderScheduler periodically checks for and dispatches due reminders.
func StartReminderScheduler(s *discordgo.Session) {
	log.Info("Starting reminder scheduler...")
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		log.Debug("Reminder scheduler ticked. Checking for due reminders...")
		processDueReminders(s)
	}
}

// processDueReminders fetches and handles due reminders.
func processDueReminders(s *discordgo.Session) {
	dueReminders, err := pb.GetDueReminders()
	if err != nil {
		log.Errorf("Error fetching due reminders: %v", err)
		return
	}
	if len(dueReminders) == 0 {
		log.Debug("No due reminders found.")
		return
	}
	log.Infof("Found %d due reminder(s). Processing...", len(dueReminders))
	for _, reminder := range dueReminders {
		var mentions []string
		for _, userID := range reminder.TargetUserIDs {
			mentions = append(mentions, fmt.Sprintf("<@%s>", userID))
		}
		var scheduledTime time.Time
		if reminder.IsRecurring {
			scheduledTime = reminder.NextReminderTime
		} else {
			scheduledTime = reminder.ReminderTime
		}
		if !scheduledTime.IsZero() {
			fullMessage := fmt.Sprintf("%s Hey! Here's your reminder: %s", strings.Join(mentions, " "), reminder.Message)
			fullMessage += fmt.Sprintf(" (Scheduled for: %s)", scheduledTime.In(reminderLocation).Format("Jan 2, 2006 at 3:04 PM (Europe/Zagreb)"))
			_, err := s.ChannelMessageSend(reminder.ChannelID, fullMessage)
			if err != nil {
				log.Errorf("Failed to send reminder message for reminder ID %s to channel %s: %v", reminder.ID, reminder.ChannelID, err)
				continue
			}
			log.Infof("Sent reminder ID %s to channel %s for users %v.", reminder.ID, reminder.ChannelID, reminder.TargetUserIDs)
			if !reminder.IsRecurring {
				err := pb.DeleteReminder(reminder.ID)
				if err != nil {
					log.Errorf("Failed to delete non-recurring reminder ID %s: %v", reminder.ID, err)
				} else {
					log.Infof("Deleted non-recurring reminder ID %s.", reminder.ID)
				}
			} else {
				nextTime, errCalc := CalculateNextRecurrence(reminder.ReminderTime, reminder.RecurrenceRule, reminder.LastTriggeredAt)
				if errCalc != nil {
					log.Errorf("Failed to calculate next recurrence for reminder ID %s: %v. Deleting reminder to prevent loop.", reminder.ID, errCalc)
					pb.DeleteReminder(reminder.ID)
					continue
				}
				reminder.NextReminderTime = nextTime
				reminder.LastTriggeredAt = time.Now().In(reminderLocation)
				errUpdate := pb.UpdateReminder(reminder)
				if errUpdate != nil {
					log.Errorf("Failed to update recurring reminder ID %s with next time %v: %v", reminder.ID, nextTime, errUpdate)
				} else {
					log.Infof("Updated recurring reminder ID %s. Next occurrence: %s", reminder.ID, nextTime.In(reminderLocation).Format(time.RFC1123))
				}
			}
		}
	}
}

// CalculateNextRecurrence calculates the next time for a recurring reminder.
func CalculateNextRecurrence(originalReminderTime time.Time, rule string, lastTriggeredTime time.Time) (time.Time, error) {
	now := time.Now().In(reminderLocation)
	baseTime := lastTriggeredTime
	if baseTime.IsZero() {
		baseTime = originalReminderTime
	}
	rule = strings.ToLower(strings.TrimSpace(rule))
	if rule == "every day" {
		hour := originalReminderTime.Hour()
		minute := originalReminderTime.Minute()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	}
	if strings.HasPrefix(rule, "every ") {
		dayPart := strings.TrimPrefix(rule, "every ")
		dayMap := map[string]time.Weekday{
			"monday":    time.Monday,
			"mon":       time.Monday,
			"tuesday":   time.Tuesday,
			"tue":       time.Tuesday,
			"wednesday": time.Wednesday,
			"wed":       time.Wednesday,
			"thursday":  time.Thursday,
			"thu":       time.Thursday,
			"friday":    time.Friday,
			"fri":       time.Friday,
			"saturday":  time.Saturday,
			"sat":       time.Saturday,
			"sunday":    time.Sunday,
			"sun":       time.Sunday,
		}
		targetDay, exists := dayMap[dayPart]
		if exists {
			hour := originalReminderTime.Hour()
			minute := originalReminderTime.Minute()
			currentDay := now.Weekday()
			daysUntil := int(targetDay - currentDay)
			if daysUntil <= 0 {
				daysUntil += 7
			}
			next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
			next = next.AddDate(0, 0, daysUntil)
			return next, nil
		}
	}
	re := regexp.MustCompile(`^every (\d+) (minutes|hours|days)$`)
	matches := re.FindStringSubmatch(rule)
	if len(matches) == 3 {
		value, err := strconv.Atoi(matches[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("internal error parsing number from rule '%s': %v", rule, err)
		}
		unit := matches[2]
		var durationToAdd time.Duration
		switch unit {
		case "minutes":
			durationToAdd = time.Duration(value) * time.Minute
		case "hours":
			durationToAdd = time.Duration(value) * time.Hour
		case "days":
			durationToAdd = time.Duration(value) * time.Hour * 24
		default:
			return time.Time{}, fmt.Errorf("unknown unit in recurrence rule: %s", unit)
		}
		next := baseTime.Add(durationToAdd)
		for !next.After(now) {
			next = next.Add(durationToAdd)
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("unsupported recurrence rule for auto-calculation: '%s'", rule)
}

// ButtonHandler handles reminder delete button interactions.
func ButtonHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionMessageComponent {
		customID := i.MessageComponentData().CustomID
		if strings.HasPrefix(customID, "reminder_delete_") {
			reminderID := strings.TrimPrefix(customID, "reminder_delete_")
			userID := getUserID(i)
			reminder, err := pb.GetReminderByID(reminderID)
			if err != nil {
				respondWithMessage(s, i, "Could not find the reminder to delete.")
				return
			}
			if reminder.UserID != userID {
				respondWithMessage(s, i, "You can only delete reminders you created.")
				return
			}
			err = pb.DeleteReminder(reminderID)
			if err != nil {
				respondWithMessage(s, i, "Failed to delete the reminder. Please try again.")
				return
			}
			respondWithMessage(s, i, "Reminder deleted successfully.")
		}
	}
}

// getUserID is a helper to get user ID from InteractionCreate (works for DMs and guilds)
func getUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}
