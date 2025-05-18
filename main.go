package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

const (
	CREATE_USER_BOOKMARKS_TABLE = `
CREATE TABLE IF NOT EXISTS user_bookmarks (
    guild_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    message_link TEXT NOT NULL,
    dm_message_id TEXT NOT NULL,
    bookmark_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, message_id)
);`

	CREATE_BOOKMARKED_MESSAGES_TABLE = `
CREATE TABLE IF NOT EXISTS bookmarked_messages (
    guild_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    message_author_id TEXT NOT NULL,
    message_link TEXT NOT NULL,
    bookmark_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (guild_id, message_id)
);`

	CHECK_BOOKMARK_EXISTS_QUERY = `
SELECT 1 FROM user_bookmarks WHERE user_id = ? AND message_id = ?;`

	INSERT_USER_BOOKMARK_QUERY = `
INSERT INTO user_bookmarks (guild_id, channel_id, user_id, message_id, message_link, dm_message_id)
VALUES (?, ?, ?, ?, ?, ?);`

	UPDATE_BOOKMARK_COUNT_QUERY = `
INSERT INTO bookmarked_messages (guild_id, channel_id, message_id, message_author_id, message_link, bookmark_count)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (guild_id, message_id) DO UPDATE SET bookmark_count = bookmark_count + 1;`
	DELETE_USER_BOOKMARK_QUERY = `
DELETE FROM user_bookmarks WHERE user_id = ? AND dm_message_id = ?;`

	DECREMENT_BOOKMARK_COUNT_QUERY = `
UPDATE bookmarked_messages 
SET bookmark_count = bookmark_count - 1 
WHERE message_id = (
    SELECT message_id FROM user_bookmarks WHERE user_id = ? AND dm_message_id = ?
);`

	DELETE_ZERO_BOOKMARKS_QUERY = `
DELETE FROM bookmarked_messages WHERE bookmark_count <= 0;`

	GET_DM_MESSAGE_ID_QUERY = `
SELECT dm_message_id, channel_id FROM user_bookmarks WHERE user_id = ? AND message_id = ?;`
)

const (
	BOOKMARK_EMOJI = "🔖"
	DELETE_EMOJI   = "❌"
)

var (
	db     *sql.DB
	logger *log.Logger
)

func main() {
	logFile, err := os.OpenFile("bookmark-bot.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}
	defer logFile.Close()
	logger = log.New(logFile, "", log.Ldate|log.Ltime|log.Lshortfile)

	err = godotenv.Load()
	if err != nil {
		logger.Println("Warning: Error loading .env file, using system environment variables")
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		logger.Fatal("DISCORD_TOKEN not set in environment")
	}

	db, err = sql.Open("sqlite3", "./bookmarks.db")
	if err != nil {
		logger.Fatalf("Error opening database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(CREATE_USER_BOOKMARKS_TABLE); err != nil {
		logger.Fatalf("Failed to create user_bookmarks table: %v", err)
	}
	if _, err := db.Exec(CREATE_BOOKMARKED_MESSAGES_TABLE); err != nil {
		logger.Fatalf("Failed to create bookmarked_messages table: %v", err)
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		logger.Fatalf("Error creating Discord session: %v", err)
	}

	dg.AddHandler(reactionAdd)
	dg.AddHandler(dmReactionAdd)
	dg.AddHandler(reactionRemove)

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

func reactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.UserID == s.State.User.ID {
		return
	}

	channelInfo, err := s.Channel(r.ChannelID)
	if err != nil {
		logger.Printf("Error getting channel info: %v", err)
		return
	}

	if channelInfo.Type == discordgo.ChannelTypeDM {
		return
	}

	if r.Emoji.Name != BOOKMARK_EMOJI {
		return
	}

	msg, err := s.ChannelMessage(r.ChannelID, r.MessageID)
	if err != nil {
		logger.Printf("Error fetching message: %v", err)
		return
	}

	user, err := s.User(r.UserID)
	if err != nil {
		logger.Printf("Error fetching user: %v", err)
		return
	}

	guild, err := s.Guild(channelInfo.GuildID)
	if err != nil {
		logger.Printf("Error fetching guild info: %v", err)
		return
	}

	var exists int
	err = db.QueryRow(CHECK_BOOKMARK_EXISTS_QUERY, r.UserID, r.MessageID).Scan(&exists)
	if err == nil {
		return
	} else if err != sql.ErrNoRows {
		logger.Printf("Error checking bookmark exists: %v", err)
		return
	}

	messageLink := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", channelInfo.GuildID, r.ChannelID, r.MessageID)

	embed := createBookmarkEmbed(msg, guild.Name, messageLink)

	dmChannel, err := s.UserChannelCreate(user.ID)
	if err != nil {
		logger.Printf("Error creating DM channel: %v", err)
		return
	}

	sentMsg, err := s.ChannelMessageSendEmbed(dmChannel.ID, embed)
	if err != nil {
		logger.Printf("Error sending DM embed: %v", err)
		return
	}

	err = s.MessageReactionAdd(dmChannel.ID, sentMsg.ID, DELETE_EMOJI)
	if err != nil {
		logger.Printf("Error adding removal reaction to DM: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		logger.Printf("Error beginning transaction: %v", err)
		return
	}

	_, err = tx.Exec(INSERT_USER_BOOKMARK_QUERY,
		channelInfo.GuildID, r.ChannelID, r.UserID, r.MessageID, messageLink, sentMsg.ID)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error inserting user bookmark: %v", err)
		return
	}

	_, err = tx.Exec(UPDATE_BOOKMARK_COUNT_QUERY,
		channelInfo.GuildID, r.ChannelID, r.MessageID, msg.Author.ID, messageLink)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error updating bookmark count: %v", err)
		return
	}

	if err = tx.Commit(); err != nil {
		logger.Printf("Error committing transaction: %v", err)
		return
	}
}

func dmReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.UserID == s.State.User.ID {
		return
	}

	channelInfo, err := s.Channel(r.ChannelID)
	if err != nil {
		logger.Printf("Error getting channel info: %v", err)
		return
	}

	if channelInfo.Type != discordgo.ChannelTypeDM {
		return
	}

	if r.Emoji.Name != DELETE_EMOJI {
		return
	}

	var messageID string
	var channelID string
	err = db.QueryRow("SELECT message_id, channel_id FROM user_bookmarks WHERE user_id = ? AND dm_message_id = ?",
		r.UserID, r.MessageID).Scan(&messageID, &channelID)
	if err != nil {
		if err == sql.ErrNoRows {
			return
		}
		logger.Printf("Error getting bookmark message ID: %v", err)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		logger.Printf("Error beginning transaction: %v", err)
		return
	}

	_, err = tx.Exec(DECREMENT_BOOKMARK_COUNT_QUERY, r.UserID, r.MessageID)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error decrementing bookmark count: %v", err)
		return
	}

	_, err = tx.Exec(DELETE_USER_BOOKMARK_QUERY, r.UserID, r.MessageID)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error deleting user bookmark: %v", err)
		return
	}

	_, err = tx.Exec(DELETE_ZERO_BOOKMARKS_QUERY)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error deleting zero bookmark messages: %v", err)
		return
	}

	if err = tx.Commit(); err != nil {
		logger.Printf("Error committing transaction: %v", err)
		return
	}

	err = s.MessageReactionRemove(channelID, messageID, BOOKMARK_EMOJI, r.UserID)
	if err != nil {
		logger.Printf("Error removing bookmark reaction from original message: %v", err)
	}

	err = s.ChannelMessageDelete(r.ChannelID, r.MessageID)
	if err != nil {
		logger.Printf("Error deleting bookmark message: %v", err)
	}
}

func reactionRemove(s *discordgo.Session, r *discordgo.MessageReactionRemove) {
	if r.UserID == s.State.User.ID {
		return
	}

	channelInfo, err := s.Channel(r.ChannelID)
	if err != nil {
		logger.Printf("Error getting channel info: %v", err)
		return
	}

	if channelInfo.Type == discordgo.ChannelTypeDM {
		return
	}

	if r.Emoji.Name != BOOKMARK_EMOJI {
		return
	}

	var dmMessageID string
	var dmChannelID string
	err = db.QueryRow(GET_DM_MESSAGE_ID_QUERY, r.UserID, r.MessageID).Scan(&dmMessageID, &dmChannelID)
	if err != nil {
		if err == sql.ErrNoRows {
			return
		}
		logger.Printf("Error finding DM message ID: %v", err)
		return
	}

	user, err := s.User(r.UserID)
	if err != nil {
		logger.Printf("Error fetching user: %v", err)
		return
	}

	dmChannel, err := s.UserChannelCreate(user.ID)
	if err != nil {
		logger.Printf("Error creating DM channel: %v", err)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		logger.Printf("Error beginning transaction: %v", err)
		return
	}

	_, err = tx.Exec("UPDATE bookmarked_messages SET bookmark_count = bookmark_count - 1 WHERE message_id = ?", r.MessageID)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error decrementing bookmark count: %v", err)
		return
	}

	_, err = tx.Exec("DELETE FROM user_bookmarks WHERE user_id = ? AND message_id = ?", r.UserID, r.MessageID)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error deleting user bookmark: %v", err)
		return
	}

	_, err = tx.Exec(DELETE_ZERO_BOOKMARKS_QUERY)
	if err != nil {
		tx.Rollback()
		logger.Printf("Error deleting zero bookmark messages: %v", err)
		return
	}

	if err = tx.Commit(); err != nil {
		logger.Printf("Error committing transaction: %v", err)
		return
	}

	err = s.ChannelMessageDelete(dmChannel.ID, dmMessageID)
	if err != nil {
		logger.Printf("Error deleting bookmark message from DM: %v", err)
	}
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
