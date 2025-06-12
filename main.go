package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

const (
	BOOKMARK_EMOJI = "🔖"
	DELETE_EMOJI   = "❌"
)

var (
	logger *log.Logger
)

func main() {
	logFile, err := os.OpenFile("bookmark-bot.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	defer logFile.Close()
	logger = log.New(logFile, "", log.Ldate|log.Ltime|log.Lshortfile)
	godotenv.Load()

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		logger.Fatal("DISCORD_TOKEN not set in environment")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		logger.Fatalf("Error creating Discord session: %v", err)
	}

	dg.AddHandler(reactionAdd)
	dg.AddHandler(dmReactionAdd)

	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions

	err = dg.Open()
	if err != nil {
		logger.Fatalf("Error opening connection: %v", err)
	}
	defer dg.Close()

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func extractMessageInfoFromLink(messageLink string) (channelID, messageID string, ok bool) {
	parts := strings.Split(messageLink, "/")
	if len(parts) < 3 {
		logger.Printf("Error: Invalid message link format: %s", messageLink)
		return "", "", false
	}
	
	messageID = parts[len(parts)-1]
	channelID = parts[len(parts)-2]
	
	return channelID, messageID, true
}

func reactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.UserID == s.State.User.ID {
		return
	}

	channelInfo, err := s.Channel(r.ChannelID)
	if err != nil {
		logger.Printf("Error getting channel info for channel %s: %v", r.ChannelID, err)
		return
	}

	if channelInfo.Type == discordgo.ChannelTypeDM {
		return
	}

	if r.Emoji.Name != BOOKMARK_EMOJI {
		return
	}

	logger.Printf("Processing bookmark reaction from user %s in channel %s:%s", r.UserID, r.ChannelID, r.MessageID)

	msg, err := s.ChannelMessage(r.ChannelID, r.MessageID)
	if err != nil {
		logger.Printf("Error getting message %s from channel %s: %v", r.MessageID, r.ChannelID, err)
		return
	}

	user, err := s.User(r.UserID)
	if err != nil {
		logger.Printf("Error getting user info for user %s: %v", r.UserID, err)
		return
	}

	guild, err := s.Guild(channelInfo.GuildID)
	if err != nil {
		logger.Printf("Error getting guild info for guild %s: %v", channelInfo.GuildID, err)
		return
	}

	messageLink := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", channelInfo.GuildID, r.ChannelID, r.MessageID)

	embed := createBookmarkEmbed(msg, guild.Name, messageLink)

	dmChannel, err := s.UserChannelCreate(user.ID)
	if err != nil {
		logger.Printf("Error creating DM channel with user %s (%s): %v", user.Username, user.ID, err)
		return
	}

	sentMsg, err := s.ChannelMessageSendEmbed(dmChannel.ID, embed)
	if err != nil {
		logger.Printf("Error sending bookmark embed to user %s (%s): %v", user.Username, user.ID, err)
		return
	}

	err = s.MessageReactionAdd(dmChannel.ID, sentMsg.ID, DELETE_EMOJI)
	if err != nil {
		logger.Printf("Error adding delete reaction to bookmark message for user %s: %v", user.Username, err)
	}

	logger.Printf("Successfully sent bookmark to user %s (%s) from guild %s", user.Username, user.ID, guild.Name)
}

func dmReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.UserID == s.State.User.ID {
		return
	}

	channelInfo, err := s.Channel(r.ChannelID)
	if err != nil {
		logger.Printf("Error getting DM channel info for channel %s: %v", r.ChannelID, err)
		return
	}

	if channelInfo.Type != discordgo.ChannelTypeDM {
		return
	}

	if r.Emoji.Name != DELETE_EMOJI {
		return
	}

	logger.Printf("Processing delete reaction from user %s in DM", r.UserID)

	msg, err := s.ChannelMessage(r.ChannelID, r.MessageID)
	if err != nil {
		logger.Printf("Error getting DM message %s from channel %s: %v", r.MessageID, r.ChannelID, err)
		return
	}

	if len(msg.Embeds) == 0 {
		logger.Printf("Warning: User %s reacted to delete on a message with no embeds", r.UserID)
		return
	}

	embed := msg.Embeds[0]
	var messageLink string
	
	for _, field := range embed.Fields {
		if field.Name == "Source" {
			start := strings.Index(field.Value, "(")
			end := strings.Index(field.Value, ")")
			if start != -1 && end != -1 && end > start {
				messageLink = field.Value[start+1 : end]
			}
			break
		}
	}

	if messageLink == "" {
		logger.Printf("Error: Could not extract message link from bookmark embed for user %s", r.UserID)
		return
	}

	channelID, messageID, ok := extractMessageInfoFromLink(messageLink)
	if !ok {
		logger.Printf("Error: Failed to parse message link %s for user %s", messageLink, r.UserID)
		return
	}

	err = s.MessageReactionRemove(channelID, messageID, BOOKMARK_EMOJI, r.UserID)
	if err != nil {
		logger.Printf("Error removing bookmark reaction from original message (channel: %s, message: %s, user: %s): %v", channelID, messageID, r.UserID, err)
	}

	err = s.ChannelMessageDelete(r.ChannelID, r.MessageID)
	if err != nil {
		logger.Printf("Error deleting bookmark message from DM (channel: %s, message: %s): %v", r.ChannelID, r.MessageID, err)
		return
	}

	logger.Printf("Successfully processed bookmark deletion for user %s", r.UserID)
}

func createBookmarkEmbed(msg *discordgo.Message, guildName, messageLink string) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Bookmark from %s", guildName),
		Description: msg.Content,
		Timestamp:   msg.Timestamp.Format(time.RFC3339),
		Color:       0x3498db,
		Author: &discordgo.MessageEmbedAuthor{
			Name:    msg.Author.Username,
			IconURL: msg.Author.AvatarURL(""),
		},
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Source",
				Value:  fmt.Sprintf("[Jump to message](%s)", messageLink),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "React with ❌ to remove this bookmark",
		},
	}

	for _, a := range msg.Attachments {
		if strings.HasPrefix(a.ContentType, "image/") {
			embed.Image = &discordgo.MessageEmbedImage{URL: a.URL}
			break
		}
	}

	for i, a := range msg.Attachments {
		if embed.Image != nil && embed.Image.URL == a.URL {
			continue
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("Attachment %d", i+1),
			Value:  fmt.Sprintf("[%s](%s)", a.Filename, a.URL),
			Inline: false,
		})
	}

	return embed
}
