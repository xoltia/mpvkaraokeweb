package mpvwebkaraoke

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type QueueHandler struct {
	queue            *Queue
	sessions         *SessionStore
	listeners        []chan<- queueEvent
	listenersMu      sync.RWMutex
	maxUserQueueSize int
}

type eventType string

const (
	RerenderQueue eventType = "queue:set"
	AppendQueue   eventType = "queue:push"
	RemoveQueue   eventType = "queue:remove"
	SessionJoin   eventType = "session:join"
)

type queueEvent struct {
	Event   eventType
	Song    Song
	SongID  int
	Session Session
}

func NewQueueHandler(queue *Queue, sessions *SessionStore, maxUserQueueSize int) *QueueHandler {
	h := &QueueHandler{
		queue:            queue,
		sessions:         sessions,
		listeners:        make([]chan<- queueEvent, 0),
		maxUserQueueSize: maxUserQueueSize,
	}

	queue.OnPush(func(s Song) {
		h.sendEvent(queueEvent{Event: AppendQueue, Song: s})
	})

	queue.OnRemove(func(id int) {
		h.sendEvent(queueEvent{Event: RemoveQueue, SongID: id})
	})

	sessions.OnCreate(func(s Session) {
		h.sendEvent(queueEvent{Event: SessionJoin, Session: s})
	})

	return h
}

func (h *QueueHandler) addListener(c chan<- queueEvent) {
	h.listenersMu.Lock()
	defer h.listenersMu.Unlock()
	h.listeners = append(h.listeners, c)
}

func (h *QueueHandler) removeListener(c chan<- queueEvent) {
	h.listenersMu.Lock()
	defer h.listenersMu.Unlock()
	for i, l := range h.listeners {
		if l == c {
			h.listeners = append(h.listeners[:i], h.listeners[i+1:]...)
			break
		}
	}
}

func (h *QueueHandler) sendEvent(e queueEvent) {
	log.Println("sending event", e)
	h.listenersMu.RLock()
	defer h.listenersMu.RUnlock()
	for _, l := range h.listeners {
		l <- e
	}
}

func (h *QueueHandler) renderQueueLocked(ctx context.Context) (html string, unlock func() error, err error) {
	songs, unlock, err := h.queue.ListLocked()
	if err != nil {
		return
	}

	table := &strings.Builder{}
	queueTable(songs).Render(ctx, table)
	html = table.String()
	return
}

func (h *QueueHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	songs, err := h.queue.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	queuePage(songs).Render(r.Context(), w)
}

func (h *QueueHandler) HandleSubmissionPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	songURL := q.Get("url")
	lyricsURL := q.Get("lyricsURL")

	postPage(songURL, lyricsURL).Render(r.Context(), w)
}

func (h *QueueHandler) HandlePostPreview(w http.ResponseWriter, r *http.Request) {
	songURL := r.FormValue("url")
	lyricsURL := r.FormValue("lyricsURL")

	video, err := getVideoInfo(r.Context(), songURL)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	submitPreview(
		video.title,
		songURL,
		lyricsURL,
		video.thumbnail,
		video.duration,
	).Render(r.Context(), w)
}

func (h *QueueHandler) HandlePostSubmission(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionKey).(Session)
	title := r.FormValue("title")
	songURL := r.FormValue("url")
	lyricsURL := r.FormValue("lyricsURL")
	durationString := r.FormValue("duration")
	thumbnail := r.FormValue("thumbnailURL")

	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}

	if songURL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	duration, err := time.ParseDuration(durationString)
	if err != nil {
		http.Error(w, "invalid duration", http.StatusBadRequest)
		return
	}

	song := Song{
		Requester: session,
		Title:     title,
		URL:       songURL,
		Duration:  duration,
		LyricsURL: sql.NullString{String: lyricsURL, Valid: lyricsURL != ""},
		Thumbnail: thumbnail,
	}

	if session.Admin {
		err = h.queue.Push(&song)
	} else {
		err = h.queue.PushLimitUser(&song, h.maxUserQueueSize)
	}

	if errors.Is(err, ErrLimitExceeded) {
		fmt.Fprint(w, "<span class=\"p-2\">You must wait for your song to be played before submitting another.</span>")
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (h *QueueHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	contentType := strings.ToLower(r.Header.Get("Accept"))

	if contentType != "text/event-stream" {
		http.Error(w, "expected Content-Type: text/event-stream", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	c := make(chan queueEvent)

	t, unlock, err := h.renderQueueLocked(r.Context())
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.addListener(c)

	if err = unlock(); err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	defer h.removeListener(c)

	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", RerenderQueue, t)
	w.(http.Flusher).Flush()

	htmlBuilder := &strings.Builder{}

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-c:
			switch event.Event {
			case AppendQueue:
				htmlBuilder.Reset()
				songRow(event.Song).Render(r.Context(), htmlBuilder)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", AppendQueue, htmlBuilder.String())
				fmt.Fprint(w, "event: queue:change\ndata:\n\n")
			case RemoveQueue:
				fmt.Fprintf(w, "event: %s:%d\ndata:\n\n", RemoveQueue, event.SongID)
				fmt.Fprint(w, "event: queue:change\ndata:\n\n")
			case SessionJoin:
				fmt.Fprintf(w, "event: %s\ndata:%s\n\n", SessionJoin, event.Session.ID)
			}
			w.(http.Flusher).Flush()
		}
	}
}

func (h *QueueHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionKey).(Session)
	if !session.Admin {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	idString := r.PathValue("id")
	id, err := strconv.Atoi(idString)
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	err = h.queue.Revoke(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *QueueHandler) HandleCurrentSong(w http.ResponseWriter, r *http.Request) {
	lastDequeud, err := h.queue.LastDequeued()

	if errors.Is(err, sql.ErrNoRows) {
		// No song has been dequeued yet.
		currentlyPlaying(nil, false).Render(r.Context(), w)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	currentlyPlaying(&lastDequeud, false).Render(r.Context(), w)
}

func (h *QueueHandler) HandleMemberList(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.sessions.GetAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	queue, err := h.queue.List()

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	members := make([]Member, len(sessions))

	for i, s := range sessions {
		if s.Admin {
			members[i] = Member{
				Session:   s,
				QueueOpen: true,
			}
			continue
		}

		count := 0
		for _, q := range queue {
			if q.Requester.ID == s.ID {
				count++
			}
		}

		members[i] = Member{
			Session:   s,
			QueueOpen: count < h.maxUserQueueSize,
		}
	}

	membersList(members).Render(r.Context(), w)
}
