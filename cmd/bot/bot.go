package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"os"
	"time"

	"github.com/Hopertz/topgames/certs"
	db "github.com/Hopertz/topgames/db/sqlc"
	"github.com/Hopertz/topgames/poll"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

const (
	start_txt       = "Use this bot to get link of top rated games between players of rating 2600's and above. Type /stop to stop receiving notifications`"
	stop_txt        = "Sorry to see you leave. Type /start to receive"
	unknown_cmd     = "I don't know that command"
	maintenance_txt = "Service will resume shortly"
	private_only    = "This bot works in private chats only. Message @TopRapidBot directly and type /start"
	connectTimeout  = 30 * time.Second
	updatesTimeout  = 60  
	httpTimeout     = 90 * time.Second 
)

func init() {

	var programLevel = new(slog.LevelVar)
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(h))

}

func main() {

	var is_maintenance_txt = false
	var bot_token string
	var database_url string
	var redis_url string

	flag.StringVar(&bot_token, "bot-token", os.Getenv("TG_BOT_TOKEN"), "Bot Token")
	flag.StringVar(&database_url, "database-url", os.Getenv("DATABASE_URL"), "Postgres connection string")
	flag.StringVar(&redis_url, "redis-url", os.Getenv("REDIS_URL"), "Redis connection string")
	flag.Parse()

	if bot_token == "" {
		slog.Error("Bot token or API url not provided")
		return
	}

	if database_url == "" || redis_url == "" {
		slog.Error("Database url or redis url not provided")
		return
	}

	bot, err := tgbotapi.NewBotAPIWithClient(bot_token, tgbotapi.APIEndpoint, &http.Client{Timeout: httpTimeout})
	if err != nil {
		slog.Error("failed to create bot api instance", "err", err)
		return
	}

	con, err := initPostgres(database_url)

	if err != nil {
		slog.Error("failed to connect to sql DB", "err", err)
		return
	}

	rdb, err := initRedis(redis_url)

	if err != nil {
		slog.Error("failed to connect to redis", "err", err)
		return
	}

	swbot := poll.New(bot, db.New(con), rdb)

	u := tgbotapi.NewUpdate(0)

	u.Timeout = updatesTimeout
	updates := bot.GetUpdatesChan(u)

	go swbot.PollTopGames()

	for update := range updates {
		if update.Message == nil {
			continue
		}

		if !update.Message.IsCommand() {
			continue
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")

		if !update.Message.Chat.IsPrivate() {
			msg.Text = private_only
			if _, err := swbot.Bot.Send(msg); err != nil {
				slog.Error("failed to send msg", "err", err, "msg", msg)
			}
			continue
		}

		switch update.Message.Command() {
		case "start":
			msg.Text = start_txt
			botUser := db.InsertTgBotUsersParams{
				ID:       update.Message.From.ID,
				Isactive: true,
			}
			err := swbot.DB.InsertTgBotUsers(context.Background(), botUser)

			if err != nil {
				slog.Error("failed to insert bot user", "err", err, "args", botUser)
			}

		case "stop":
			botUser := db.UpdateTgBotUsersParams{
				ID:       update.Message.From.ID,
				Isactive: false,
			}
			err := swbot.DB.UpdateTgBotUsers(context.Background(), botUser)
			if err != nil {
				slog.Error("failed to update bot user", "err", err, "args", botUser)
			}
			msg.Text = stop_txt
		case "subs":
			res, err := swbot.DB.GetActiveTgBotUsers(context.Background())
			if err != nil {
				slog.Error("failed to get bot active members", "err", err)
			}
			msg.Text = fmt.Sprintf("There are %d subscribers in @TopRapidBot", len(res))

		case "list":
			count, err := swbot.SentGamesCount()
			if err != nil {
				slog.Error("failed to count sent games", "err", err)
			}
			msg.Text = fmt.Sprintf("There are %d in a list so far.", count)

		case "sm":
			if poll.Master_ID == update.Message.From.ID {
				is_maintenance_txt = true
			}else {
				msg.Text = "Only @Hopertz is allowed"
			}

		case "help":
			msg.Text = `
			Commands for this @TopRapidBot are:
			
			/start  start the bot
			/stop   stop the bot 
			/subs    total subscribers
			/list    current games in a list
			/help   this message
			/sm     send maintanance msg			`

		default:
			msg.Text = unknown_cmd
		}

		if is_maintenance_txt {
			swbot.SendMaintananceMsg(maintenance_txt)
			is_maintenance_txt = false

		} else {
			if _, err := swbot.Bot.Send(msg); err != nil {
				slog.Error("failed to send msg", "err", err, "msg", msg)
			}
		}

	}
}

// TLS settings that only trust servers signed by the Schema certificate authority
func schemaTLS(host string) (*tls.Config, error) {

	pool := x509.NewCertPool()

	if !pool.AppendCertsFromPEM(certs.SchemaCA) {
		return nil, errors.New("failed to load schema ca certificate")
	}

	return &tls.Config{
		RootCAs:    pool,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}, nil
}

func initPostgres(databaseURL string) (*sql.DB, error) {

	cfg, err := pgx.ParseConfig(databaseURL)

	if err != nil {
		slog.Error("Error parsing database url", "err", err)
		return nil, err
	}

	cfg.TLSConfig, err = schemaTLS(cfg.Host)

	if err != nil {
		return nil, err
	}

	db := stdlib.OpenDB(*cfg)

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	stmt := `CREATE TABLE IF NOT EXISTS tgbot_users (
               id BIGINT NOT NULL PRIMARY KEY,
               isactive BOOLEAN NOT NULL
            );`

	_, err = db.ExecContext(ctx, stmt)

	if err != nil {
		slog.Error("Error executing create table stmt", "err", err)
		return nil, err
	}

	return db, nil

}

func initRedis(redisURL string) (*redis.Client, error) {

	opt, err := redis.ParseURL(redisURL)

	if err != nil {
		slog.Error("Error parsing redis url", "err", err)
		return nil, err
	}

	host, _, err := net.SplitHostPort(opt.Addr)

	if err != nil {
		slog.Error("Error reading redis host", "err", err)
		return nil, err
	}

	opt.TLSConfig, err = schemaTLS(host)

	if err != nil {
		return nil, err
	}

	rdb := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Error pinging redis", "err", err)
		return nil, err
	}

	return rdb, nil

}
