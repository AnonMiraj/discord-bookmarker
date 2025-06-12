# 📌 discord-bookmarker

A lightweight discord bot that lets users bookmark messages by reacting with 🔖 and manage them via DMs. Bookmarked messages are saved in a local SQLite database and can be removed by reacting with ❌.

## Setup

1. **Clone the repo:**

   ```bash
   git clone https://github.com/yourusername/discord-bookmarker.git
   cd discord-bookmarker
   ```

2. **Create a `.env` file** with your bot token:

   ```env
   DISCORD_TOKEN=your_bot_token_here
   ```

3. **Run the bot:**

   ```bash
   go run main.go
   ```

## Installation

Install dependencies:

```bash
go get github.com/bwmarrin/discordgo
go get github.com/joho/godotenv
```

## Logging

Logs are written to `bookmark-bot.log` in the same directory.

## License

MIT License


