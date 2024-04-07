package mpvwebkaraoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"sync"

	"github.com/wader/goutubedl"
)

type OnceCache interface {
	Cache(context.Context, string)
	GetOrCancel(string) (string, bool)
	Clear(string) error
}

type noOpCache struct{}

func (noOpCache) Cache(ctx context.Context, key string) {}
func (noOpCache) GetOrCancel(key string) (string, bool) { return "", false }
func (noOpCache) Clear(key string) error                { return nil }

var NullCache = OnceCache(&noOpCache{})

type cacheStatus uint8

const (
	cacheStatusPending cacheStatus = iota
	cacheStatusDownloading
	cacheStatusAvailable
	cacheStatusFailed
)

type cacheJob struct {
	url string
	ctx context.Context
}

type cacheEntry struct {
	status cacheStatus
	cancel context.CancelFunc
}

type VideoCacheConfig struct {
	DownloadFilter string
	CachePath      string
}

type VideoCache struct {
	config    VideoCacheConfig
	entries   map[string]*cacheEntry
	entriesMu sync.Mutex
	queue     chan cacheJob
	queueOnce sync.Once
}

func NewVideoCache(config VideoCacheConfig) *VideoCache {
	return &VideoCache{
		config:  config,
		entries: make(map[string]*cacheEntry),
	}
}

type readerWithContext struct {
	io.Reader
	ctx context.Context
}

func (cr *readerWithContext) Read(p []byte) (n int, err error) {
	if cr.ctx.Err() != nil {
		return 0, cr.ctx.Err()
	}

	return cr.Reader.Read(p)
}

// Cache caches a video from a URL. If the video is already being cached, it does nothing.
func (vc *VideoCache) Cache(ctx context.Context, vidURL string) {
	log.Println("queuing", vidURL)

	vc.entriesMu.Lock()
	if _, ok := vc.entries[vidURL]; ok {
		vc.entriesMu.Unlock()
		log.Println("already caching", vidURL)
		return
	}

	ctxCancel, cancel := context.WithCancel(ctx)

	vc.entries[vidURL] = &cacheEntry{cancel: cancel, status: cacheStatusPending}
	vc.entriesMu.Unlock()

	vc.queueOnce.Do(func() {
		log.Println("starting queue worker")
		vc.queue = make(chan cacheJob, 512)
		go vc.queueWorker()
	})

	vc.queue <- cacheJob{url: vidURL, ctx: ctxCancel}
}

// GetOrCache returns the path to the cached video if it is available, or cancels the download if it is pending.
func (vc *VideoCache) GetOrCancel(vidURL string) (string, bool) {
	vc.entriesMu.Lock()
	defer vc.entriesMu.Unlock()

	if entry, ok := vc.entries[vidURL]; ok {
		if entry.status == cacheStatusAvailable {
			return vc.vidCachePath(vidURL), true
		}

		log.Println("calling cancel", vidURL)
		entry.cancel()
	}

	return "", false
}

// Clear cancels the download of a video and removes it from the cache.
func (vc *VideoCache) Clear(vidURL string) error {
	log.Println("clearing", vidURL)

	vc.entriesMu.Lock()
	defer vc.entriesMu.Unlock()

	if entry, ok := vc.entries[vidURL]; ok {
		entry.cancel()
	}

	vc.entries[vidURL] = nil
	delete(vc.entries, vidURL)
	if err := vc.removeArtifacts(vidURL); err != nil {
		return fmt.Errorf("failed to remove artifacts: %w", err)
	}

	return nil
}

func (vc *VideoCache) removeArtifacts(vidURL string) error {
	vidPath := vc.vidCachePath(vidURL)
	if _, err := os.Stat(vidPath); err == nil {
		return os.Remove(vidPath)
	}

	return nil
}

func (vc *VideoCache) download(ctx context.Context, vidURL string) (err error) {
	log.Println("downloading", vidURL)

	result, err := goutubedl.New(ctx, vidURL, goutubedl.Options{})
	if err != nil {
		err = fmt.Errorf("failed to create youtube-dl result: %w", err)
		return
	}

	video, err := result.Download(ctx, vc.config.DownloadFilter)

	if err != nil {
		err = fmt.Errorf("failed to download video: %w", err)
		return
	}

	defer video.Close()

	tempFile, err := os.CreateTemp(vc.config.CachePath, "video_cache_*")
	if err != nil {
		err = fmt.Errorf("failed to create temp file: %w", err)
		return
	}

	defer tempFile.Close()

	_, err = io.Copy(tempFile, &readerWithContext{video, ctx})
	if err != nil {
		err = fmt.Errorf("failed to write to temp file: %w", err)
		removeErr := os.Remove(tempFile.Name())
		if removeErr != nil {
			removeErr = fmt.Errorf("failed to remove temp file: %w", removeErr)
			err = errors.Join(err, removeErr)
		}
		return
	}

	err = os.Rename(tempFile.Name(), vc.vidCachePath(vidURL))

	if err != nil {
		err = fmt.Errorf("failed to rename temp file: %w", err)
	}

	return
}

func (vc *VideoCache) setStatusOnExisting(key string, status cacheStatus) {
	vc.entriesMu.Lock()
	defer vc.entriesMu.Unlock()
	if _, ok := vc.entries[key]; ok {
		vc.entries[key].status = status
	}
}

func (vc *VideoCache) clearEntry(key string) {
	vc.entriesMu.Lock()
	defer vc.entriesMu.Unlock()
	vc.entries[key] = nil
	delete(vc.entries, key)
}

func (vc *VideoCache) queueWorker() {
	for job := range vc.queue {
		// don't start downloading if the context is already canceled
		if job.ctx.Err() != nil {
			log.Println("skipping download for", job.url)
			vc.clearEntry(job.url)
			continue
		}

		vc.setStatusOnExisting(job.url, cacheStatusDownloading)
		err := vc.download(job.ctx, job.url)

		if err != nil {
			// if the context is canceled, it is suitable
			// for retry should it come up again
			select {
			case <-job.ctx.Done():
				log.Println("download canceled for", job.url)
				vc.clearEntry(job.url)
			default:
				log.Println("download failed for", job.url, ":", err)
				vc.setStatusOnExisting(job.url, cacheStatusFailed)
			}
			continue
		}

		log.Println("downloaded", job.url)
		vc.setStatusOnExisting(job.url, cacheStatusAvailable)
	}
}

func (vc *VideoCache) vidCachePath(vidURL string) string {
	return path.Join(vc.config.CachePath, url.PathEscape(vidURL))
}
