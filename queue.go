package mpvwebkaraoke

import (
	"database/sql"
	"sync"
	"time"
)

type Song struct {
	ID        int
	Requester User
	Title     string
	Thumbnail string
	URL       string
	LyricsURL sql.NullString
	Duration  time.Duration
}

type PushEventHandler func(Song)
type RemoveEventHandler func(int)

type Queue struct {
	mu             sync.RWMutex
	cond           *sync.Cond
	id             int
	userLimit      int
	current        []Song
	dequeued       []Song
	revoked        []Song
	pushHandlers   []PushEventHandler
	removeHandlers []RemoveEventHandler
}

func NewQueue(perUserLimit int) *Queue {
	q := &Queue{}
	q.userLimit = perUserLimit
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue) Push(song Song) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	var c int

	if song.Requester.Admin {
		goto push
	}

	for _, s := range q.current {
		if s.Requester.ID == song.Requester.ID {
			c++
		}
	}

	if c == q.userLimit {
		return false
	}

push:
	song.ID = q.id
	q.current = append(q.current, song)
	q.id++

	for _, h := range q.pushHandlers {
		h(song)
	}

	q.cond.Signal()
	return true
}

func (q *Queue) List() []Song {
	q.mu.RLock()
	defer q.mu.RUnlock()
	songs := make([]Song, len(q.current))
	copy(songs, q.current)
	return songs
}

func (q *Queue) Freeze() (songs []Song, unlock func()) {
	q.mu.RLock()
	songs = make([]Song, len(q.current))
	copy(songs, q.current)
	return songs, q.mu.RUnlock
}

func (q *Queue) Revoke(id int) (ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, song := range q.current {
		if song.ID == id {
			q.current = append(q.current[:i], q.current[i+1:]...)
			q.revoked = append(q.revoked, song)

			for _, h := range q.removeHandlers {
				h(id)
			}

			return true
		}
	}

	return false
}

func (q *Queue) Dequeue() Song {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.current) == 0 {
		q.cond.Wait()
	}

	song := q.current[0]
	q.current = q.current[1:]
	q.dequeued = append(q.dequeued, song)

	for _, h := range q.removeHandlers {
		h(song.ID)
	}

	return song
}

func (q *Queue) LastDequeued() (Song, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.dequeued) == 0 {
		return Song{}, false
	}

	return q.dequeued[len(q.dequeued)-1], true
}

func (q *Queue) OnPush(h PushEventHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pushHandlers = append(q.pushHandlers, h)
}

func (q *Queue) OnRemove(h RemoveEventHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.removeHandlers = append(q.removeHandlers, h)
}
