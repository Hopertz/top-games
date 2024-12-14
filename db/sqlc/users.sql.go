// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.26.0
// source: users.sql

package db

import (
	"context"
)

const getActiveTgBotUsers = `-- name: GetActiveTgBotUsers :many
SELECT id from tgbot_users WHERE isactive = true
`

func (q *Queries) GetActiveTgBotUsers(ctx context.Context) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, getActiveTgBotUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const insertTgBotUsers = `-- name: InsertTgBotUsers :exec
INSERT INTO tgbot_users (id, isactive) VALUES (?, ?)
`

type InsertTgBotUsersParams struct {
	ID       int64 `json:"id"`
	Isactive bool  `json:"isactive"`
}

func (q *Queries) InsertTgBotUsers(ctx context.Context, arg InsertTgBotUsersParams) error {
	_, err := q.db.ExecContext(ctx, insertTgBotUsers, arg.ID, arg.Isactive)
	return err
}

const updateTgBotUsers = `-- name: UpdateTgBotUsers :exec
UPDATE tgbot_users SET isactive = ? WHERE id = ?
`

type UpdateTgBotUsersParams struct {
	Isactive bool  `json:"isactive"`
	ID       int64 `json:"id"`
}

func (q *Queries) UpdateTgBotUsers(ctx context.Context, arg UpdateTgBotUsersParams) error {
	_, err := q.db.ExecContext(ctx, updateTgBotUsers, arg.Isactive, arg.ID)
	return err
}
