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

// parseWhenSimple is a basic parser for "in Xm/h/d" and other supported formats.
func parseWhenSimple(whenStr string) (time.Time, bool, string, error) {
	whenStr = strings.ToLower(strings.TrimSpace(whenStr))
	now := time.Now().UTC().In(reminderLocation)
	isRecurring := false
	recurrenceRule := ""

	// Check for "every" keyword for recurrence
	if strings.HasPrefix(whenStr, "every ") {
		isRecurring = true
		whenStr = strings.TrimPrefix(whenStr, "every ")
	}

	// Regex for "in Xunit" or "Xunit"
	re := regexp.MustCompile(`^(?:in\s+)?(\d+)\s*([mhd])$`)
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

	// For now, only "in Xm/h/d" is supported for non-recurring.
	// And "every Xm/h/d" for recurring.
	if isRecurring {
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
	now := time.Now().UTC().In(reminderLocation)
	currentDay := now.Weekday()
	daysUntil := int(targetDay - currentDay)
	if daysUntil <= 0 {
		daysUntil += 7 // Move to next week
	}
	return now.AddDate(0, 0, daysUntil), nil
}
