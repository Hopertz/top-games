package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	db "github.com/Hopertz/topgames/db/sqlc"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

type SWbot struct {
	Bot   *tgbotapi.BotAPI
	Links *map[string]time.Time
	mu    sync.RWMutex
	DB    db.Queries
	queue chan outMsg
}


type outMsg struct {
	text       string
	skipMaster bool
}

func New(bot *tgbotapi.BotAPI, queries *db.Queries) *SWbot {

	links := make(map[string]time.Time)

	return &SWbot{
		Bot:   bot,
		Links: &links,
		DB:    *queries,
		queue: make(chan outMsg, queueSize),
	}
}

const (
	base_url         = "https://lichess.org/"
	minLinkStayInMap = 1 * time.Hour
	cleanUpTime      = 30 * time.Minute
	Master_ID        = 731217828
	queueSize        = 100
	sendInterval     = 40 * time.Millisecond
	minRoundTime     = 1 * time.Second
)

type Member struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Online    bool   `json:"online"`
	Playing   bool   `json:"playing"`
	Streaming bool   `json:"streaming"`
	Patron    bool   `json:"patron"`
	PlayingId string `json:"playingId"`
}


// Poll games after every one minute, filter by rating (>2600), store them if not present in a map  & notify bot users
func (sw *SWbot) PollTopGames() {

	ticker := time.NewTicker(time.Minute * 1)
	defer ticker.Stop()

	go sw.cleanUpMap(sw.Links)
	go sw.sender()

	for range ticker.C {

		games := fetchTopGames()

		for _, game := range games {

			sw.mu.RLock()
			_, idExists := (*sw.Links)[game.ID]
			sw.mu.RUnlock()

			if game.Rated && game.Status == "started" && !idExists {
				w := game.Players.White.Rating
				b := game.Players.Black.Rating

				if w >= 2600 && b >= 2600 {
					sw.mu.Lock()
					(*sw.Links)[game.ID] = time.Now()
					sw.mu.Unlock()
					sw.SendMsgToTelegramIds(game.ID)

				} 

			}

		}

	}
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

// Delete links that have stayed in the map for more than 1 hour
func (sw *SWbot) cleanUpMap(links *map[string]time.Time) {
	// Run the clean up every 30 minutes
	ticker := time.NewTicker(cleanUpTime)

	defer ticker.Stop()

	for range ticker.C {
		sw.mu.Lock()
		for lichessId, timeAtStart := range *links {
			if time.Since(timeAtStart) > minLinkStayInMap {
				delete(*links, lichessId)
			}
		}
		sw.mu.Unlock()
	}
}

// Number of games currently tracked in the map
func (sw *SWbot) LinksCount() int {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return len(*sw.Links)
}


func (sw *SWbot) SendMsgToTelegramIds(linkId string) {
	sw.enqueue(outMsg{text: fmt.Sprintf("%s%s", base_url, linkId)})
}

func (sw *SWbot) SendMaintananceMsg(msg string) {
	sw.enqueue(outMsg{text: msg, skipMaster: true})
}


func (sw *SWbot) enqueue(m outMsg) {

	select {
	case sw.queue <- m:
	default:
		slog.Error("send queue is full, dropping msg", "text", m.text)
	}
}


func (sw *SWbot) sender() {

	pace := time.NewTicker(sendInterval)
	defer pace.Stop()

	for m := range sw.queue {

		start := time.Now()

		ids, err := sw.DB.GetActiveTgBotUsers(context.Background())
		if err != nil {
			slog.Error("failed to get active bot users", "err", err, "text", m.text)
			continue
		}

		for _, id := range ids {
			if m.skipMaster && id == Master_ID {
				continue
			}

			<-pace.C
			sw.sendToUser(id, m.text)
		}

		time.Sleep(minRoundTime - time.Since(start))
	}
}

// Send a text to user. When telegram says we are too fast (429) wait
// as long as it asks and retry once. A user who blocked the bot or deleted
// their account (403) is marked inactive so we stop sending to them.
func (sw *SWbot) sendToUser(id int64, text string) {

	msg := tgbotapi.NewMessage(id, text)

	_, err := sw.Bot.Send(msg)

	var tgErr *tgbotapi.Error
	if errors.As(err, &tgErr) && tgErr.Code == http.StatusTooManyRequests {
		time.Sleep(time.Duration(tgErr.RetryAfter) * time.Second)
		_, err = sw.Bot.Send(msg)
	}

	if err == nil {
		return
	}

	slog.Error("failed to send msg", "err", err, "user", id)

	if errors.As(err, &tgErr) && tgErr.Code == http.StatusForbidden {
		args := db.UpdateTgBotUsersParams{ID: id, Isactive: false}
		if err := sw.DB.UpdateTgBotUsers(context.Background(), args); err != nil {
			slog.Error("failed to deactivate bot user", "err", err, "user", id)
		}
	}
}
