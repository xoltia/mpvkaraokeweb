package mpvwebkaraoke

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

type Session struct {
	ID       string
	UserName string
	Admin    bool
}

type SessionStore struct {
	db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db}
}

func (s *SessionStore) Init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			user_name TEXT NOT NULL,
			admin BOOLEAN NOT NULL
		)
	`)
	return err
}

func (s *SessionStore) Create(user string, admin bool) (id string, err error) {
	id, err = newSessionID()
	if err != nil {
		return
	}

	_, err = s.db.Exec(`
		INSERT INTO sessions (id, user_name, admin)
		VALUES (?, ?, ?)
	`, id, user, admin)
	return
}

func (s *SessionStore) Get(id string) (session Session, err error) {
	err = s.db.QueryRow(`
		SELECT id, user_name, admin
		FROM sessions
		WHERE id = ?
	`, id).Scan(&session.ID, &session.UserName, &session.Admin)
	return
}

func newSessionID() (id string, err error) {
	b := make([]byte, 16)
	_, err = rand.Read(b)
	if err != nil {
		return
	}
	id = hex.EncodeToString(b)
	return
}
