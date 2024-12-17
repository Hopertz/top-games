package poll

import (
	"context"
	"encoding/json"
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
}

const (
	base_url         = "https://lichess.org/"
	minLinkStayInMap = 1 * time.Hour
	cleanUpTime      = 30 * time.Minute
	Master_ID        = 731217828
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

func (sw *SWbot) PollTopGames() {

	ticker := time.NewTicker(time.Minute * 1)
	defer ticker.Stop()

	go sw.cleanUpMap(sw.Links)

	for range ticker.C {

		games := fetchTopGames()

		for _, game := range games {

			sw.mu.RLock()
			_, idExists := (*sw.Links)[game.ID]
			sw.mu.RUnlock()

			if game.Rated && game.Status == "started" && !idExists {
				w := game.Players.White.Rating
				b := game.Players.Black.Rating

				if w >= 2500 && b >= 2500 {
					sw.mu.Lock()
					(*sw.Links)[game.ID] = time.Now()
					sw.mu.Unlock()
					sw.SendMsgToTelegramIds(game.ID)

				} 

			}

		}

	}
}

func fetchTopGames() []Game {

	var games []Game

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", top_games_url, nil)

	if err != nil {
		slog.Error("failed to create request fetchtopgames", "error", err)
		return games
	}

	req.Header.Set("Accept", "application/x-ndjson")

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("failed to fetch top games", "err", err)
		return games
	}

	defer resp.Body.Close()

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
		for lichessId, timeAtStart := range *links {
			if time.Since(timeAtStart) > minLinkStayInMap {
				sw.mu.Lock()
				delete(*links, lichessId)
				sw.mu.Unlock()

			}
		}
	}
}

func (sw *SWbot) SendMsgToTelegramIds(linkId string) {

	ids, _ := sw.DB.GetActiveTgBotUsers(context.Background())
	for _, id := range ids {
		msg := tgbotapi.NewMessage(id, fmt.Sprintf("%s%s", base_url, linkId))

		sw.Bot.Send(msg)
	}
}

// Send a message to all active users when the bot is going for maintanance
func (sw *SWbot) SendMaintananceMsg(msg string) {

	ids, _ := sw.DB.GetActiveTgBotUsers(context.Background())
	for _, id := range ids {
		if id != Master_ID {
			msg := tgbotapi.NewMessage(id, msg)
			sw.Bot.Send(msg)
		}

	}
}
