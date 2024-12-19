package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"os"
	"time"

	db "github.com/Hopertz/topgames/db/sqlc"
	"github.com/Hopertz/topgames/poll"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/mattn/go-sqlite3"
)

const (
	start_txt       = "Use this bot to get link of top rated games between players of rating 2500's and above. Type /stop to stop receiving notifications`"
	stop_txt        = "Sorry to see you leave. Type /start to receive"
	unknown_cmd     = "I don't know that command"
	maintenance_txt = "Service will resume shortly"
	usersDB         = "users.db"
)

func init() {

	var programLevel = new(slog.LevelVar)
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(h))

}

func main() {

	var is_maintenance_txt = false
	var bot_token string

	flag.StringVar(&bot_token, "bot-token", os.Getenv("TG_BOT_TOKEN"), "Bot Token")
	flag.Parse()

	if bot_token == "" {
		slog.Error("Bot token or API url not provided")
		return
	}

	bot, err := tgbotapi.NewBotAPI(bot_token)
	if err != nil {
		slog.Error("failed to create bot api instance", "err", err)
		return
	}

	con, err := initdB()

	if err != nil {
		slog.Error("failed to connect to sql DB", "err", err)
		return
	}

	links := make(map[string]time.Time)

	swbot := &poll.SWbot{
		Bot:   bot,
		Links: &links,
		DB:    *db.New(con),
	}

	u := tgbotapi.NewUpdate(0)

	u.Timeout = 60
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

		switch update.Message.Command() {
		case "start":
			msg.Text = start_txt
			botUser := db.InsertTgBotUsersParams{
				ID:       update.Message.From.ID,
				Isactive: true,
			}
			err := swbot.DB.InsertTgBotUsers(context.Background(), botUser)

			if err != nil {
				switch {
				case err.Error() == `pq: duplicate key value violates unique constraint "tgbot_users_pkey"`:
					args := db.UpdateTgBotUsersParams{
						ID:       botUser.ID,
						Isactive: botUser.Isactive,
					}
					err := swbot.DB.UpdateTgBotUsers(context.Background(), args)
					if err != nil {
						slog.Error("failed to update bot user", "err", err, "args", args)
					}

				default:
					slog.Error("failed to insert bot user", "err", err, "args", botUser)
				}
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

		case "ml":
			msg.Text = fmt.Sprintf("There are %d in a list so far.", len(*swbot.Links))

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
			/ml     current games in a list
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
func initdB() (*sql.DB, error) {

	_, err := os.Stat(usersDB)

	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, err := os.Create(usersDB)
			if err != nil {
				slog.Error("failed to create db file", "err", err)
				return nil, err
			}

		} else {
			slog.Error("Error getting info db file", "err", err)
			return nil, err
		}

	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("./%s", usersDB))

	if err != nil {
		slog.Error("Error opening db file", "err", err)
		return nil, err
	}

	stmt := `CREATE TABLE IF NOT EXISTS tgbot_users (
               id INTEGER NOT NULL PRIMARY KEY,
               isactive BOOLEAN NOT NULL
            );`

	_, err = db.Exec(stmt)

	if err != nil {
		slog.Error("Error executing create table stmt", "err", err)
		return nil, err
	}

	return db, nil

}
