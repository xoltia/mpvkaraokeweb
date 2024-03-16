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
	db             *sql.DB
	createListener func(Session)
}

func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db, nil}
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

func (s *SessionStore) OnCreate(f func(Session)) {
	s.createListener = f
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

	if err == nil && s.createListener != nil {
		s.createListener(Session{ID: id, UserName: user, Admin: admin})
	}

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

func (s *SessionStore) GetAll() (sessions []Session, err error) {
	rows, err := s.db.Query(`
		SELECT id, user_name, admin
		FROM sessions
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var session Session
		err = rows.Scan(&session.ID, &session.UserName, &session.Admin)
		if err != nil {
			return
		}
		sessions = append(sessions, session)
	}
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
