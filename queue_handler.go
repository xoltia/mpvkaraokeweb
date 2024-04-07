package mpvwebkaraoke

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type QueueHandler struct {
	queue            *Queue
	listeners        []chan<- queueEvent
	listenersMu      sync.RWMutex
	maxUserQueueSize int
	connections      map[User]int
	connectionsMu    sync.RWMutex
}

type eventType string

const (
	RerenderQueue eventType = "queue:set"
	AppendQueue   eventType = "queue:push"
	RemoveQueue   eventType = "queue:remove"
	SessionJoin   eventType = "session:join"
	SessionLeave  eventType = "session:leave"
)

type queueEvent struct {
	Event  eventType
	Song   Song
	SongID int
	User   User
}

func NewQueueHandler(queue *Queue, maxUserQueueSize int) *QueueHandler {
	h := &QueueHandler{
		queue:            queue,
		listeners:        make([]chan<- queueEvent, 0),
		maxUserQueueSize: maxUserQueueSize,
		connections:      make(map[User]int),
	}

	queue.OnPush(func(s Song) {
		h.sendEvent(queueEvent{Event: AppendQueue, Song: s})
	})

	queue.OnRemove(func(id int) {
		h.sendEvent(queueEvent{Event: RemoveQueue, SongID: id})
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
	h.listenersMu.RLock()
	defer h.listenersMu.RUnlock()
	for _, l := range h.listeners {
		l <- e
	}
}

func (h *QueueHandler) renderQueueLocked(ctx context.Context) (html string, unlock func()) {
	songs, unlock := h.queue.Freeze()

	table := &strings.Builder{}
	queueTable(songs).Render(ctx, table)
	html = table.String()
	return
}

func (h *QueueHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	songs := h.queue.List()
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

func checkURL(s string) bool {
	u, err := url.Parse(s)

	if err != nil {
		return false
	}

	return u.Scheme == "http" || u.Scheme == "https"
}

func (h *QueueHandler) HandlePostSubmission(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(User)
	title := r.FormValue("title")
	songURL := r.FormValue("url")
	lyricsURL := r.FormValue("lyricsURL")
	durationString := r.FormValue("duration")
	thumbnail := r.FormValue("thumbnailURL")

	if !checkURL(songURL) {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}

	if lyricsURL != "" && !checkURL(lyricsURL) {
		http.Error(w, "invalid lyrics URL", http.StatusBadRequest)
		return
	}

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
		Requester: user,
		Title:     title,
		URL:       songURL,
		Duration:  duration,
		LyricsURL: sql.NullString{String: lyricsURL, Valid: lyricsURL != ""},
		Thumbnail: thumbnail,
	}

	ok := h.queue.Push(song)
	if !ok {
		fmt.Fprint(w, "<span class=\"p-2\">You must wait for your song to be played before submitting another.</span>")
		return
	}

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (h *QueueHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(User)
	contentType := strings.ToLower(r.Header.Get("Accept"))

	if contentType != "text/event-stream" {
		http.Error(w, "expected Content-Type: text/event-stream", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	c := make(chan queueEvent, 64)
	t, unlock := h.renderQueueLocked(r.Context())
	h.addListener(c)
	unlock()
	defer h.removeListener(c)
	h.incConnection(user)
	defer h.decConnection(user)

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
				fmt.Fprintf(w, "event: %s\ndata:%s\n\n", SessionJoin, event.User.ID)
			case SessionLeave:
				fmt.Printf("event: %s\ndata:%s\n\n", SessionJoin, event.User.ID)
				fmt.Fprintf(w, "event: %s\ndata:%s\n\n", SessionJoin, event.User.ID)
			}
			w.(http.Flusher).Flush()
		}
	}
}

func (h *QueueHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(User)
	if !user.Admin {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	idString := r.PathValue("id")
	id, err := strconv.Atoi(idString)
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	ok := h.queue.Revoke(id)
	if !ok {
		http.Error(w, "song not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *QueueHandler) HandleCurrentSong(w http.ResponseWriter, r *http.Request) {
	lastDequeud, ok := h.queue.LastDequeued()

	if !ok {
		// No song has been dequeued yet.
		currentlyPlaying(nil, false).Render(r.Context(), w)
		return
	}

	currentlyPlaying(&lastDequeud, false).Render(r.Context(), w)
}

func (h *QueueHandler) incConnection(u User) {
	h.connectionsMu.Lock()
	defer h.connectionsMu.Unlock()
	h.connections[u]++
	if h.connections[u] == 1 {
		h.sendEvent(queueEvent{Event: SessionJoin, User: u})
	}
}

func (h *QueueHandler) decConnection(u User) {
	h.connectionsMu.Lock()
	defer h.connectionsMu.Unlock()
	h.connections[u]--
	if h.connections[u] == 0 {
		h.sendEvent(queueEvent{Event: SessionLeave, User: u})
	}
}

func (h *QueueHandler) HandleMemberList(w http.ResponseWriter, r *http.Request) {
	h.connectionsMu.RLock()
	defer h.connectionsMu.RUnlock()
	sessions := h.connections
	queue := h.queue.List()
	members := make([]Member, 0, len(sessions))

	for u := range sessions {
		if u.Admin {
			members = append(members, Member{
				User:      u,
				QueueOpen: true,
			})
			continue
		}

		count := 0
		for _, q := range queue {
			if q.Requester.ID == u.ID {
				count++
			}
		}

		members = append(members, Member{
			User:      u,
			QueueOpen: count < h.maxUserQueueSize,
		})
	}

	membersList(members).Render(r.Context(), w)
}
