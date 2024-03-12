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

	"github.com/wader/goutubedl"
)

type QueueHandler struct {
	queue            *Queue
	listeners        []chan<- queueEvent
	listenersMu      sync.RWMutex
	maxUserQueueSize int
}

type eventType string

const (
	RerenderQueue eventType = "rerender-queue"
	AppendQueue   eventType = "append-queue"
	RemoveQueue   eventType = "remove-queue"
)

type queueEvent struct {
	Event  eventType
	Song   Song
	SongID int
}

func NewQueueHandler(queue *Queue, maxUserQueueSize int) *QueueHandler {
	h := &QueueHandler{
		queue:            queue,
		listeners:        make([]chan<- queueEvent, 0),
		maxUserQueueSize: maxUserQueueSize,
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
	songTable(songs).Render(ctx, table)
	html = table.String()
	return
}

func (h *QueueHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	songs, err := h.queue.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	queueHome(songs).Render(r.Context(), w)
}

func (h *QueueHandler) HandleSubmissionPage(w http.ResponseWriter, r *http.Request) {
	postPage().Render(r.Context(), w)
}

func (h *QueueHandler) HandlePostPreview(w http.ResponseWriter, r *http.Request) {
	songURL := r.FormValue("url")
	lyricsURL := r.FormValue("lyricsURL")

	result, err := goutubedl.New(r.Context(), songURL, goutubedl.Options{})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	submitPreview(
		result.Info.Title,
		songURL,
		lyricsURL,
		result.Info.Thumbnail,
		time.Duration(result.Info.Duration)*time.Second,
	).Render(r.Context(), w)
}

func (h *QueueHandler) HandlePostSubmission(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionKey).(Session)
	title := r.FormValue("title")
	songURL := r.FormValue("url")
	lyricsURL := r.FormValue("lyricsURL")
	durationString := r.FormValue("duration")

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
	}

	if session.Admin {
		err = h.queue.Push(&song)
	} else {
		err = h.queue.PushLimitUser(&song, h.maxUserQueueSize)
	}

	if errors.Is(err, ErrLimitExceeded) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Retarget", "#error")
		w.Header().Set("HX-Reswap", "innerHTML")
		fmt.Fprint(w, "<span>You must wait for your song to be played before submitting another.</span>")
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
			case RemoveQueue:
				fmt.Fprintf(w, "event: %s-%d\ndata:\n\n", RemoveQueue, event.SongID)
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
