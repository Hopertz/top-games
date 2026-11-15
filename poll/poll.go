package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	db "github.com/Hopertz/topgames/db/sqlc"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/redis/go-redis/v9"
)

const top_games_url = "https://lichess.org/api/tv/rapid?nb=30&moves=false&tags=false"

var lichessClient = &http.Client{
	Timeout: 10 * time.Second,
}

type Game struct {
	ID      string  `json:"id"`
	Rated   bool    `json:"rated"`
	Status  string  `json:"status"`
	Players Players `json:"players"`
}

type Players struct {
	White Player `json:"white"`
	Black Player `json:"black"`
}

type Player struct {
	Rating int `json:"rating"`
}

type TopBot struct {
	Bot   *tgbotapi.BotAPI
	DB    db.Queries
	Redis *redis.Client
	queue chan outMsg

	pollStopped chan struct{} // closed when the poller has stopped
	sendStopped chan struct{} // closed when the sender has sent what was left in the queue
}

type outMsg struct {
	text       string
	skipMaster bool
}

func New(bot *tgbotapi.BotAPI, queries *db.Queries, rdb *redis.Client) *TopBot {

	return &TopBot{
		Bot:   bot,
		DB:    *queries,
		Redis: rdb,
		queue: make(chan outMsg, queueSize),

		pollStopped: make(chan struct{}),
		sendStopped: make(chan struct{}),
	}
}

const (
	base_url       = "https://lichess.org/"
	sentGamePrefix = "sent:"
	sentGameTTL    = 24 * time.Hour
	redisTimeout   = 10 * time.Second
	Master_ID      = 731217828
	queueSize      = 100
	sendInterval   = 40 * time.Millisecond
	minRoundTime   = 1 * time.Second
)

// Poll games after every one minute, filter by rating (>2600), record them in redis if not present & notify bot users
// Stops when ctx is cancelled.
func (tb *TopBot) PollTopGames(ctx context.Context) {

	defer close(tb.pollStopped)

	ticker := time.NewTicker(time.Minute * 1)
	defer ticker.Stop()

	go tb.sender(ctx)

	for {

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		games := fetchTopGames()

		for _, game := range games {

			// Stop recording new games once we are shutting down
			if ctx.Err() != nil {
				return
			}

			if !game.Rated || game.Status != "started" {
				continue
			}

			w := game.Players.White.Rating
			b := game.Players.Black.Rating

			if w < 2600 || b < 2600 {
				continue
			}

			isNew, err := tb.markGameSent(game.ID)
			if err != nil {
				slog.Error("failed to record game in redis", "err", err, "game", game.ID)
				continue
			}

			if isNew {
				tb.SendMsgToTelegramIds(game.ID)
			}

		}

	}
}

// Record a game in redis, the key deletes itself after sentGameTTL.
// Returns true when the game was not recorded before.
func (tb *TopBot) markGameSent(gameId string) (bool, error) {

	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()

	err := tb.Redis.SetArgs(ctx, sentGamePrefix+gameId, time.Now().Unix(), redis.SetArgs{
		Mode: "NX",
		TTL:  sentGameTTL,
	}).Err()

	// redis.Nil means the key already exists
	if errors.Is(err, redis.Nil) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return true, nil
}

// Number of games recorded in redis
func (tb *TopBot) SentGamesCount() (int64, error) {

	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()

	return tb.Redis.DBSize(ctx).Result()
}

// Fetch top rapid games on lichess and store them in list
func fetchTopGames() []Game {

	var games []Game

	req, err := http.NewRequest("GET", top_games_url, nil)

	if err != nil {
		slog.Error("failed to create request fetchtopgames", "error", err)
		return games
	}

	req.Header.Set("Accept", "application/x-ndjson")

	resp, err := lichessClient.Do(req)
	if err != nil {
		slog.Error("failed to fetch top games", "err", err)
		return games
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("lichess returned an error status", "status", resp.Status)
		return games
	}

	results := json.NewDecoder(resp.Body)

	for {

		var game Game

		err := results.Decode(&game)

		if err != nil {
			if err != io.EOF {
				slog.Error("we got an error while reading", "err", err)
			}

			break
		}

		games = append(games, game)

	}

	return games

}

func (tb *TopBot) SendMsgToTelegramIds(linkId string) {
	tb.enqueue(outMsg{text: fmt.Sprintf("%s%s", base_url, linkId)})
}

func (tb *TopBot) SendMaintananceMsg(msg string) {
	tb.enqueue(outMsg{text: msg, skipMaster: true})
}

func (tb *TopBot) enqueue(m outMsg) {

	select {
	case tb.queue <- m:
	default:
		slog.Error("send queue is full, dropping msg", "text", m.text)
	}
}

func (tb *TopBot) sender(ctx context.Context) {

	defer close(tb.sendStopped)

	pace := time.NewTicker(sendInterval)
	defer pace.Stop()

	for {

		select {
		case m := <-tb.queue:
			tb.broadcast(m, pace)

		case <-ctx.Done():
			// Shutting down: wait for the poller so nothing new is queued,
			// send what is left in the queue, then stop
			<-tb.pollStopped

			for {
				select {
				case m := <-tb.queue:
					tb.broadcast(m, pace)
				default:
					return
				}
			}
		}
	}
}

// Send one queued message to all active users
func (tb *TopBot) broadcast(m outMsg, pace *time.Ticker) {

	start := time.Now()

	ids, err := tb.DB.GetActiveTgBotUsers(context.Background())
	if err != nil {
		slog.Error("failed to get active bot users", "err", err, "text", m.text)
		return
	}

	for _, id := range ids {
		if m.skipMaster && id == Master_ID {
			continue
		}

		<-pace.C
		tb.sendToUser(id, m.text)
	}

	time.Sleep(minRoundTime - time.Since(start))
}

// Wait until the sender has sent what was left in the queue.
// Returns false when it did not finish within the timeout.
func (tb *TopBot) WaitStopped(timeout time.Duration) bool {

	select {
	case <-tb.sendStopped:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Send a text to user. When telegram says we are too fast (429) wait
// as long as it asks and retry once. A user who blocked the bot or deleted
// their account (403) is marked inactive so we stop sending to them.
func (tb *TopBot) sendToUser(id int64, text string) {

	msg := tgbotapi.NewMessage(id, text)

	_, err := tb.Bot.Send(msg)

	var tgErr *tgbotapi.Error
	if errors.As(err, &tgErr) && tgErr.Code == http.StatusTooManyRequests {
		time.Sleep(time.Duration(tgErr.RetryAfter) * time.Second)
		_, err = tb.Bot.Send(msg)
	}

	if err == nil {
		return
	}

	slog.Error("failed to send msg", "err", err, "user", id)

	if errors.As(err, &tgErr) && tgErr.Code == http.StatusForbidden {
		args := db.UpdateTgBotUsersParams{ID: id, Isactive: false}
		if err := tb.DB.UpdateTgBotUsers(context.Background(), args); err != nil {
			slog.Error("failed to deactivate bot user", "err", err, "user", id)
		}
	}
}
