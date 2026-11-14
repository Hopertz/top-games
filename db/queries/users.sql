-- name: InsertTgBotUsers :exec
INSERT INTO tgbot_users (id, isactive) VALUES (?, ?)
ON CONFLICT (id) DO UPDATE SET isactive = excluded.isactive;

-- name: UpdateTgBotUsers :exec
UPDATE tgbot_users SET isactive = ? WHERE id = ?;

-- name: GetActiveTgBotUsers :many
SELECT id from tgbot_users WHERE isactive = true;