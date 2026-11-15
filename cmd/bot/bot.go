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
	"os/signal"
	"syscall"
	"time"

	"github.com/Hopertz/topgames/certs"
	"github.com/Hopertz/topgames/db/migrations"
	db "github.com/Hopertz/topgames/db/sqlc"
	"github.com/Hopertz/topgames/poll"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

const (
	start_txt   = "Use this bot to get link of top rated games between players of rating 2600's and above. Type /stop to stop receiving notifications"
	stop_txt    = "Sorry to see you leave. You will no longer receive game links. Type /start to receive them again."
	unknown_cmd = "I don't know that command"
	master_only = "Only @Hopertz is allowed"
	failed_txt  = "Something went wrong, check the logs"
	help_txt    = `Commands for this @TopRapidBot are:

/start  start the bot
/stop   stop the bot
/help   this message`
	master_help_txt = `

Owner commands:

/subs   total subscribers
/list   current games in a list
/sm     send maintanance msg`
	maintenance_txt = "Service will resume shortly"
	private_only    = "This bot works in private chats only. Message @TopRapidBot directly and type /start"
	connectTimeout  = 30 * time.Second
	shutdownTimeout = 30 * time.Second
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

	topbot := poll.New(bot, db.New(con), rdb)

	u := tgbotapi.NewUpdate(0)

	u.Timeout = updatesTimeout
	updates := bot.GetUpdatesChan(u)

	// ctx is cancelled when systemd stops the service (SIGTERM) or on Ctrl+C
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go topbot.PollTopGames(ctx)

	slog.Info("bot started")

loop:
	for {

		var update tgbotapi.Update

		select {
		case <-ctx.Done():
			break loop
		case update = <-updates:
		}

		if update.Message == nil {
			continue
		}

		if !update.Message.IsCommand() {
			continue
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")

		if !update.Message.Chat.IsPrivate() {
			msg.Text = private_only
			if _, err := topbot.Bot.Send(msg); err != nil {
				slog.Error("failed to send msg", "err", err, "msg", msg)
			}
			continue
		}

		// Some commands are for the bot owner only
		isMaster := update.Message.From.ID == poll.Master_ID

		switch update.Message.Command() {
		case "start":
			msg.Text = start_txt
			botUser := db.InsertTgBotUsersParams{
				ID:       update.Message.From.ID,
				Isactive: true,
			}
			err := topbot.DB.InsertTgBotUsers(context.Background(), botUser)

			if err != nil {
				slog.Error("failed to insert bot user", "err", err, "args", botUser)
			}

		case "stop":
			botUser := db.UpdateTgBotUsersParams{
				ID:       update.Message.From.ID,
				Isactive: false,
			}
			err := topbot.DB.UpdateTgBotUsers(context.Background(), botUser)
			if err != nil {
				slog.Error("failed to update bot user", "err", err, "args", botUser)
			}
			msg.Text = stop_txt
		case "subs":
			if !isMaster {
				msg.Text = master_only
				break
			}

			res, err := topbot.DB.GetActiveTgBotUsers(context.Background())
			if err != nil {
				slog.Error("failed to get bot active members", "err", err)
				msg.Text = failed_txt
				break
			}
			msg.Text = fmt.Sprintf("There are %d subscribers in @TopRapidBot", len(res))

		case "list":
			if !isMaster {
				msg.Text = master_only
				break
			}

			count, err := topbot.SentGamesCount()
			if err != nil {
				slog.Error("failed to count sent games", "err", err)
				msg.Text = failed_txt
				break
			}
			msg.Text = fmt.Sprintf("There are %d in a list so far.", count)

		case "sm":
			if isMaster {
				is_maintenance_txt = true
			} else {
				msg.Text = master_only
			}

		case "help":
			msg.Text = help_txt
			if isMaster {
				msg.Text += master_help_txt
			}

		default:
			msg.Text = unknown_cmd
		}

		if is_maintenance_txt {
			topbot.SendMaintananceMsg(maintenance_txt)
			is_maintenance_txt = false

		} else {
			if _, err := topbot.Bot.Send(msg); err != nil {
				slog.Error("failed to send msg", "err", err, "msg", msg)
			}
		}

	}

	slog.Info("shutting down, sending what is left in the queue")

	// A second signal from here on stops the bot at once
	stop()

	if !topbot.WaitStopped(shutdownTimeout) {
		slog.Error("shutdown timed out, some msgs were not sent")
	}

	rdb.Close()
	con.Close()

	slog.Info("bot stopped")
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

	if err := runMigrations(cfg); err != nil {
		slog.Error("Error running migrations", "err", err)
		return nil, err
	}

	return stdlib.OpenDB(*cfg), nil

}

// Apply the migrations in db/migrations that have not been applied yet.
// The applied version is kept in the schema_migrations table.
func runMigrations(cfg *pgx.ConnConfig) error {

	// migrate closes the connection it is given, so it gets its own
	db := stdlib.OpenDB(*cfg)

	source, err := iofs.New(migrations.Files, ".")

	if err != nil {
		db.Close()
		return err
	}

	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})

	if err != nil {
		db.Close()
		return err
	}

	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)

	if err != nil {
		db.Close()
		return err
	}

	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	return nil
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
