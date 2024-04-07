package main

import (
	"context"
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/NYTimes/gziphandler"
	"github.com/UTD-JLA/mpvwebkaraoke"
	"github.com/UTD-JLA/mpvwebkaraoke/style"
	"github.com/gorilla/sessions"
	"github.com/wader/goutubedl"
	"golang.ngrok.com/ngrok"
	"golang.ngrok.com/ngrok/config"
	"golang.org/x/oauth2"
)

var (
	dbPath         = flag.String("db", "karaoke.sqlite", "path to sqlite database")
	cachePath      = flag.String("cache", "vidcache", "path to video cache")
	disableCache   = flag.Bool("disable-cache", false, "disable video cache")
	disablePersist = flag.Bool("disable-persist", false, "disable queue persistence")
	ytdlPath       = flag.String("ytdl", "yt-dlp", "path to youtube-dl")
	ytdlFilter     = flag.String("ytdl-filter", "bestvideo[ext=mp4][height<=1080]+bestaudio/best", "youtube-dl filter")
	maxUserQueue   = flag.Int("max-queue", 1, "maximum number of songs a user can queue")
	noCompression  = flag.Bool("no-compression", false, "disable gzip compression")
	sessionSecret  = flag.String("session-secret", "secret", "session secret")
	clientID       = flag.String("client-id", "", "discord client ID (required)")
	clientSecret   = flag.String("client-secret", "", "discord client secret (required)")
	guildID        = flag.String("guild-id", "", "discord guild ID (required)")
	ngrokDomain    = flag.String("ngrok-domain", "", "ngrok domain (required)")
	ngrokToken     = flag.String("ngrok-token", "", "ngrok authtoken (required)")
)

// wraps with newlines if text is too long
func wrapLongText(text string, length int) string {
	chars := []rune(text)

	if len(chars) >= length {
		wrapped := string(chars[:length]) + "\n"
		wrapped += wrapLongText(string(chars[length:]), length)
		return wrapped
	}

	return text
}

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
		song := queue.Dequeue()
		log.Println("playing", song.Title)

		msg := fmt.Sprintf("Now playing:\n%s", wrapLongText(song.Title, 25))
		msg += "\n\n"
		msg += fmt.Sprintf("Requested by:\n%s", song.Requester.Name)

		previewFileName := path.Join(os.TempDir(), "preview_frame.png")

		if err := writePreviewFrame(previewFileName, msg); err != nil {
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

		if err := cache.Clear(song.URL); err != nil {
			log.Println("error clearing cache:", err)
		}
	}
}

func checkFlags() {
	if *clientID == "" {
		log.Fatal("client ID is required")
	}

	if *clientSecret == "" {
		log.Fatal("client secret is required")
	}

	if *guildID == "" {
		log.Fatal("guild ID is required")
	}

	if *ngrokDomain == "" {
		log.Fatal("ngrok domain is required")
	}

	if *ngrokToken == "" {
		log.Fatal("ngrok authtoken is required")
	}
}

func main() {
	flag.Parse()
	checkFlags()
	goutubedl.Path = *ytdlPath
	queue := mpvwebkaraoke.NewQueue(*maxUserQueue)

	if !*disablePersist {
		err := queue.Start(context.Background())
		if err != nil {
			panic(err)
		}
	}

	cacheConfig := mpvwebkaraoke.VideoCacheConfig{
		CachePath:      *cachePath,
		DownloadFilter: *ytdlFilter,
	}

	gob.Register(mpvwebkaraoke.User{})
	gob.Register(mpvwebkaraoke.Song{})
	vidCache := mpvwebkaraoke.NullCache
	if !*disableCache {
		err := os.MkdirAll(cacheConfig.CachePath, 0755)
		if err != nil {
			panic(err)
		}
		vidCache = mpvwebkaraoke.NewVideoCache(cacheConfig)
	}

	store := sessions.NewCookieStore([]byte(*sessionSecret))
	conf := &oauth2.Config{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
		// RedirectURL:  "http://localhost:8080/auth/callback",
		RedirectURL: fmt.Sprintf("https://%s/auth/callback", *ngrokDomain),
		Scopes:      []string{"identify", "guilds.members.read"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://discord.com/api/oauth2/authorize",
			TokenURL: "https://discord.com/api/oauth2/token",
		},
	}

	authHandler := mpvwebkaraoke.NewAuthHandler(store, conf, *guildID)
	queueHandler := mpvwebkaraoke.NewQueueHandler(queue, *maxUserQueue)

	queue.OnPush(func(song mpvwebkaraoke.Song) {
		vidCache.Cache(context.Background(), song.URL)
	})

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/queue", http.StatusFound)
	})

	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	mux.HandleFunc("GET /auth", authHandler.HandleIndex)
	mux.HandleFunc("GET /auth/callback", authHandler.HandleCallback)
	mux.HandleFunc("GET /auth/logout", authHandler.HandleLogout)

	if *noCompression {
		mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "max-age=31536000")
			http.ServeFileFS(w, r, style.CSSFiles, "style.css")
		})
		mux.HandleFunc("GET /queue", authHandler.Wrap(queueHandler.HandleIndex))
		mux.HandleFunc("GET /queue/request", authHandler.Wrap(queueHandler.HandleSubmissionPage))
		mux.HandleFunc("POST /queue/preview", authHandler.Wrap(queueHandler.HandlePostPreview))
		mux.HandleFunc("POST /queue/request", authHandler.Wrap(queueHandler.HandlePostSubmission))
		mux.HandleFunc("DELETE /queue/revoke/{id}", authHandler.Wrap(queueHandler.HandleRevoke))
		mux.HandleFunc("GET /queue/current", authHandler.Wrap(queueHandler.HandleCurrentSong))
		mux.HandleFunc("GET /queue/members", authHandler.Wrap(queueHandler.HandleMemberList))
		//mux.HandleFunc("GET /sse", authHandler.Wrap(queueHandler.HandleSSE))
	} else {
		mux.Handle("GET /style.css", gziphandler.GzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "max-age=31536000")
			http.ServeFileFS(w, r, style.CSSFiles, "style.css")
		})))
		mux.Handle("GET /queue", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandleIndex))))
		mux.Handle("GET /queue/request", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandleSubmissionPage))))
		mux.Handle("POST /queue/preview", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandlePostPreview))))
		mux.Handle("POST /queue/request", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandlePostSubmission))))
		mux.Handle("DELETE /queue/revoke/{id}", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandleRevoke))))
		mux.Handle("GET /queue/current", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandleCurrentSong))))
		mux.Handle("GET /queue/members", gziphandler.GzipHandler(http.HandlerFunc(authHandler.Wrap(queueHandler.HandleMemberList))))
	}

	mux.HandleFunc("GET /sse", authHandler.Wrap(queueHandler.HandleSSE))

	go loopMPV(queue, vidCache)

	listener, err := ngrok.Listen(context.Background(),
		config.HTTPEndpoint(
			config.WithDomain(*ngrokDomain),
		),
		ngrok.WithAuthtoken(*ngrokToken),
	)

	if err != nil {
		log.Fatal(err)
	}

	log.Printf("listening on https://%s\n", listener.Addr())
	http.Serve(listener, mux)
}
