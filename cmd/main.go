package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/UTD-JLA/mpvwebkaraoke"
	_ "github.com/mattn/go-sqlite3"
	"github.com/wader/goutubedl"
)

var (
	addr         = flag.String("addr", ":8080", "address to listen on")
	dbPath       = flag.String("db", "karaoke.sqlite", "path to sqlite database")
	cachePath    = flag.String("cache", "vidcache", "path to video cache")
	disableCache = flag.Bool("disable-cache", false, "disable video cache")
	ytdlPath     = flag.String("ytdl", "yt-dlp", "path to youtube-dl")
	ytdlFilter   = flag.String("ytdl-filter", "bestvideo[ext=mp4][height<=1080]+bestaudio/best", "youtube-dl filter")
	adminCode    = flag.String("admin-code", "", "code needed for registering as admin")
)

func writePreviewFrame(filename, message string) error {
	cmd := exec.Command(
		"convert",
		"-size", "1920x1080",
		"xc:#ffffff",
		"-font", "Noto-Sans-CJK-JP-Bold",
		"-pointsize", "50",
		"-fill", "black",
		"-draw", fmt.Sprintf("text 150,150 '%s'", strings.ReplaceAll(message, "'", "\\'")),
		filename,
	)

	return cmd.Run()
}

func loopMPV(queue *mpvwebkaraoke.Queue, cache mpvwebkaraoke.OnceCache) {
	for {
		song, err := queue.Shift()
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				log.Println("error getting song:", err)
			}
			time.Sleep(1 * time.Second)
			continue
		}

		log.Println("playing", song.Title)

		msg := fmt.Sprintf("Now playing:\n%s", song.Title)
		msg += "\n\n"
		msg += fmt.Sprintf("Requested by:\n%s", song.Requester.UserName)

		previewFileName := path.Join(os.TempDir(), "preview_frame.png")

		if err = writePreviewFrame(previewFileName, msg); err != nil {
			log.Println("error writing preview frame:", err)
		}

		videoFileName, ok := cache.GetOrCancel(song.URL)

		if !ok {
			videoFileName = song.URL
		}

		cmd := exec.Command(
			"mpv",
			"--pause=yes",
			"--fs",
			previewFileName,
			videoFileName,
		)

		if err := cmd.Run(); err != nil {
			log.Println("error playing song:", err)
			if exitError, ok := err.(*exec.ExitError); ok {
				stderr := string(exitError.Stderr)
				fmt.Println(stderr)
			}
		}

		if err = cache.Clear(song.URL); err != nil {
			log.Println("error clearing cache:", err)
		}
	}
}

func main() {
	flag.Parse()

	goutubedl.Path = *ytdlPath

	if *adminCode == "" {
		log.Println("warning: no admin code set, admin registration is disabled")
	}

	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	sessionStore := mpvwebkaraoke.NewSessionStore(db)
	if err = sessionStore.Init(); err != nil {
		panic(err)
	}

	queue := mpvwebkaraoke.NewQueue(db)
	if err = queue.Init(); err != nil {
		panic(err)
	}

	config := mpvwebkaraoke.VideoCacheConfig{
		CachePath:      *cachePath,
		DownloadFilter: *ytdlFilter,
	}

	vidCache := mpvwebkaraoke.NullCache
	if !*disableCache {
		err = os.MkdirAll(config.CachePath, 0755)
		if err != nil {
			panic(err)
		}
		vidCache = mpvwebkaraoke.NewVideoCache(config)
	}

	authHandler := mpvwebkaraoke.NewAuthHandler(sessionStore, *adminCode)
	queueHandler := mpvwebkaraoke.NewQueueHandler(queue)

	queue.OnPush(func(song mpvwebkaraoke.Song) {
		vidCache.Cache(context.Background(), song.URL)
	})

	mux := http.NewServeMux()

	mux.Handle("GET /register", http.HandlerFunc(authHandler.HandleIndex))
	mux.Handle("POST /register", http.HandlerFunc(authHandler.HandleRegister))
	mux.Handle("GET /", authHandler.Wrap(http.HandlerFunc(queueHandler.HandleIndex)))
	mux.Handle("POST /preview", authHandler.Wrap(http.HandlerFunc(queueHandler.HandlePostPreview)))
	mux.Handle("POST /submit", authHandler.Wrap(http.HandlerFunc(queueHandler.HandlePostSubmission)))
	mux.Handle("GET /submit", authHandler.Wrap(http.HandlerFunc(queueHandler.HandleSubmissionPage)))
	mux.Handle("GET /sse", authHandler.Wrap(http.HandlerFunc(queueHandler.HandleSSE)))
	mux.Handle("DELETE /revoke/{id}", authHandler.Wrap(http.HandlerFunc(queueHandler.HandleRevoke)))

	http.Handle("/", mux)

	go loopMPV(queue, vidCache)

	http.ListenAndServe(*addr, nil)
}
