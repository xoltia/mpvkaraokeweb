package mpvwebkaraoke

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type Song struct {
	ID         int
	Requester  Session
	Title      string
	URL        string
	LyricsURL  sql.NullString
	Duration   time.Duration
	Position   int
	CreatedAt  time.Time
	RevokedAt  sql.NullTime
	DequeuedAt sql.NullTime
}

type PushEventHandler func(Song)
type RemoveEventHandler func(int)

type Queue struct {
	db               *sql.DB
	pushHandlers     []PushEventHandler
	pushHandlersMu   sync.Mutex
	removeHandlers   []RemoveEventHandler
	removeHandlersMu sync.Mutex
}

func NewQueue(db *sql.DB) *Queue {
	return &Queue{
		db:             db,
		pushHandlers:   make([]PushEventHandler, 0),
		removeHandlers: make([]RemoveEventHandler, 0),
	}
}

func (q *Queue) OnPush(handler PushEventHandler) {
	q.pushHandlersMu.Lock()
	defer q.pushHandlersMu.Unlock()
	q.pushHandlers = append(q.pushHandlers, handler)
}

func (q *Queue) OnRemove(handler RemoveEventHandler) {
	q.removeHandlersMu.Lock()
	defer q.removeHandlersMu.Unlock()
	q.removeHandlers = append(q.removeHandlers, handler)
}

func (q *Queue) emitPush(s Song) {
	q.pushHandlersMu.Lock()
	defer q.pushHandlersMu.Unlock()
	for _, h := range q.pushHandlers {
		h(s)
	}
}

func (q *Queue) emitRemove(sID int) {
	q.removeHandlersMu.Lock()
	defer q.removeHandlersMu.Unlock()
	for _, h := range q.removeHandlers {
		h(sID)
	}
}

func (q *Queue) Init() error {
	_, err := q.db.Exec(`
		CREATE TABLE IF NOT EXISTS queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			requester_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			url TEXT NOT NULL,
			lyrics_url TEXT,
			duration INTEGER NOT NULL,
			position INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			revoked_at TIMESTAMP,
			dequeued_at TIMESTAMP,
			available GENERATED ALWAYS AS (revoked_at IS NULL AND dequeued_at IS NULL) VIRTUAL,
			FOREIGN KEY (requester_id) REFERENCES sessions(id)
		)
	`)
	return err
}

// Push adds a song to the queue.
// Sets the song's ID and CreatedAt fields.
func (q *Queue) Push(song *Song) error {
	err := q.db.QueryRow(`
			INSERT INTO queue (requester_id, title, url, lyrics_url, duration, position)
			VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM queue))
			RETURNING id, created_at
		`,
		song.Requester.ID,
		song.Title,
		song.URL,
		song.LyricsURL,
		song.Duration,
	).Scan(&song.ID, &song.CreatedAt)

	if err == nil {
		q.emitPush(*song)
	}

	return err
}

// Shift returns the next song, removing it from the queue and shifting all other songs up.
func (q *Queue) Shift() (s Song, err error) {
	ctx := context.Background()
	conn, err := q.db.Conn(ctx)

	if err != nil {
		return
	}

	defer conn.Close()

	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE TRANSACTION;")
	if err != nil {
		return
	}

	defer func() {
		if err != nil {
			conn.ExecContext(ctx, "ROLLBACK TRANSACTION;")
		} else {
			conn.ExecContext(ctx, "END TRANSACTION;")
		}
	}()

	err = conn.QueryRowContext(ctx, `
		SELECT
			s.id,
			s.requester_id,
			s.title,
			s.url,
			s.lyrics_url,
			s.duration,
			s.position,
			s.created_at,
			s.revoked_at,
			s.dequeued_at,
			u.user_name,
			u.admin
		FROM queue s
		JOIN sessions u ON s.requester_id = u.id
		WHERE s.position = 1
	`).Scan(
		&s.ID,
		&s.Requester.ID,
		&s.Title,
		&s.URL,
		&s.LyricsURL,
		&s.Duration,
		&s.Position,
		&s.CreatedAt,
		&s.RevokedAt,
		&s.DequeuedAt,
		&s.Requester.UserName,
		&s.Requester.Admin,
	)

	if err != nil {
		return
	}

	_, err = conn.ExecContext(ctx, `
		UPDATE queue
		SET dequeued_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, s.ID)

	if err != nil {
		return
	}

	q.emitRemove(s.ID)

	_, err = conn.ExecContext(ctx, `
		UPDATE queue
		SET position = position - 1
	`)

	return
}

// Revoke removes a song from the queue.
func (q *Queue) Revoke(id int) error {
	_, err := q.db.Exec(`
		UPDATE queue
		SET revoked_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)

	if err == nil {
		q.emitRemove(id)
	}

	return err
}

// List returns all songs in the queue.
func (q *Queue) List() (songs []Song, err error) {
	rows, err := q.db.Query(`
		SELECT
			s.id,
			s.requester_id,
			s.title,
			s.url,
			s.lyrics_url,
			s.duration,
			s.position,
			s.created_at,
			s.revoked_at,
			s.dequeued_at,
			u.user_name,
			u.admin
		FROM queue s
		JOIN sessions u ON s.requester_id = u.id
		WHERE s.available
		ORDER BY s.position
	`)
	if err != nil {
		return
	}

	defer rows.Close()

	for rows.Next() {
		var s Song
		err = rows.Scan(
			&s.ID,
			&s.Requester.ID,
			&s.Title,
			&s.URL,
			&s.LyricsURL,
			&s.Duration,
			&s.Position,
			&s.CreatedAt,
			&s.RevokedAt,
			&s.DequeuedAt,
			&s.Requester.UserName,
			&s.Requester.Admin,
		)
		if err != nil {
			return
		}
		songs = append(songs, s)
	}

	err = rows.Err()
	return
}

// ListLocked returns all songs in the queue, locking the rows for update, and
// a function to release the lock.
func (q *Queue) ListLocked() (songs []Song, unlock func() error, err error) {
	ctx := context.Background()
	conn, err := q.db.Conn(ctx)

	if err != nil {
		return
	}

	_, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE TRANSACTION;")
	if err != nil {
		return
	}

	defer func() {
		if err != nil {
			conn.ExecContext(ctx, "END TRANSACTION;")
			conn.Close()
		}
	}()

	rows, err := conn.QueryContext(ctx, `
		SELECT
			s.id,
			s.requester_id,
			s.title,
			s.url,
			s.lyrics_url,
			s.duration,
			s.position,
			s.created_at,
			s.revoked_at,
			s.dequeued_at,
			u.user_name,
			u.admin
		FROM queue s
		JOIN sessions u ON s.requester_id = u.id
		WHERE s.available
		ORDER BY s.position
	`)
	if err != nil {
		return
	}

	for rows.Next() {
		var s Song
		err = rows.Scan(
			&s.ID,
			&s.Requester.ID,
			&s.Title,
			&s.URL,
			&s.LyricsURL,
			&s.Duration,
			&s.Position,
			&s.CreatedAt,
			&s.RevokedAt,
			&s.DequeuedAt,
			&s.Requester.UserName,
			&s.Requester.Admin,
		)
		if err != nil {
			return
		}
		songs = append(songs, s)
	}

	if err = rows.Err(); err != nil {
		return
	}

	unlock = func() error {
		_, err := conn.ExecContext(ctx, "END TRANSACTION;")
		conn.Close()
		return err
	}

	return
}

// Move moves a song with the given ID to the given position.
func (q *Queue) Move(id, position int) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	_, err = tx.Exec(`
		UPDATE queue
		SET position = position + 1
		WHERE position >= ?
	`, position)

	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE queue
		SET position = ?
		WHERE id = ?
	`, position, id)

	return err
}
