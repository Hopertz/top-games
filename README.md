## RapidLinkGames Bot

A Telegram bot that sends links to top rapid games being played on [Lichess](https://lichess.org).
Subscribe at [@TopRapidBot](https://t.me/TopRapidBot).

### What it does

Once a minute the bot asks Lichess for the top ongoing rapid games. Every rated game
where both players are rated 2600 or above is sent, once, as a link to each subscriber.

The bot works in private chats only. Commands sent from a group get a reply asking
the user to message the bot directly.

### Commands

| Command  | Who       | What it does                              |
| -------- | --------- | ----------------------------------------- |
| `/start` | Everyone  | Subscribe to game links                   |
| `/stop`  | Everyone  | Unsubscribe                               |
| `/help`  | Everyone  | List the commands                         |
| `/subs`  | Bot owner | Number of active subscribers              |
| `/list`  | Bot owner | Number of games sent in the last 24 hours |
| `/sm`    | Bot owner | Send a maintenance message to subscribers |

### How it works

- **Subscribers** are stored in PostgreSQL. A user who blocks the bot is marked inactive.
- **Sent games** are stored in Redis, each for 24 hours, so a game is never sent twice,
  even after a restart.
- **Sending** runs from a queue at a steady pace to stay within Telegram's limits.
- **Schema changes** are numbered files in `db/migrations`. The bot applies the ones
  not yet applied when it starts. The Go code in `db/sqlc` is generated from the same
  files with `sqlc generate`.

### Configuration

The bot reads three settings, from environment variables or from flags:

| Environment variable | Flag            | Value                        |
| -------------------- | --------------- | ---------------------------- |
| `TG_BOT_TOKEN`       | `-bot-token`    | Token from @BotFather        |
| `DATABASE_URL`       | `-database-url` | PostgreSQL connection string |
| `REDIS_URL`          | `-redis-url`    | Redis connection string      |

Both databases are hosted on [Schema](https://schema.tz). Connections are encrypted and
verified against the Schema certificate authority in `certs/schema-ca.crt`, which is
built into the binary.

Never commit these values. Locally, keep them in a `.env` file, which is ignored by git.

### Running locally

Requires Go 1.26 or newer.

```sh
export TG_BOT_TOKEN=...
export DATABASE_URL=...
export REDIS_URL=...

go run ./cmd/bot
```

### Deployment

A push to `main` runs the workflow in `.github/workflows/deploy.yml`, which builds the
binary, copies it to the server and restarts `topbot.service`.

On the server the three settings live in a `.env` file next to the binary, loaded by
the service with `EnvironmentFile=`.
