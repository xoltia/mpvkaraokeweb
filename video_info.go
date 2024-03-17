package mpvwebkaraoke

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kkdai/youtube/v2"
	"github.com/wader/goutubedl"
)

type videoInfo struct {
	title     string
	thumbnail string
	duration  time.Duration
}

func getVideoInfo(ctx context.Context, vidURL string) (vid videoInfo, err error) {
	u, err := url.Parse(vidURL)

	if err != nil {
		return getVideoSlow(ctx, vidURL)
	}

	switch u.Host {
	case "youtube.com", "www.youtube.com":
		if vid, err = getYouTubeVideoFast(ctx, vidURL); err == nil {
			return
		}
		fallthrough
	default:
		var err2 error
		vid, err2 = getVideoSlow(ctx, vidURL)
		if err2 != nil {
			err = errors.Join(err, err2)
		}
		return
	}
}

func getVideoSlow(ctx context.Context, vidURL string) (vid videoInfo, err error) {
	result, err := goutubedl.New(ctx, vidURL, goutubedl.Options{})

	if err != nil {
		return
	}

	vid = videoInfo{
		title:     result.Info.Title,
		duration:  time.Duration(result.Info.Duration) * time.Second,
		thumbnail: result.Info.Thumbnail,
	}

	return
}

var thumbnailNames = [...]string{
	"maxresdefault",
	"hq720",
	"sddefault",
	"hqdefault",
	"0",
	"mqdefault",
	"default",
	"sd1",
	"sd2",
	"sd3",
	"hq1",
	"hq2",
	"hq3",
	"mq1",
	"mq2",
	"mq3",
	"1",
	"2",
	"3",
}

func selectThumbnail(vid *youtube.Video) (thumbnail string) {
	for _, name := range thumbnailNames {
		for _, ext := range []string{"webp", "jpg"} {
			for _, live := range []string{"", "_live"} {
				webp := ""
				if ext == "webp" {
					webp = "_webp"
				}
				url := fmt.Sprintf("https://i.ytimg.com/vi%s/%s/%s%s.%s", webp, vid.ID, name, live, ext)
				vid.Thumbnails = append(vid.Thumbnails, youtube.Thumbnail{URL: url})
			}
		}
	}

	highScore := 0
	highIndex := 0

	for i, t := range vid.Thumbnails {
		nameIndex := 0
		for j, name := range thumbnailNames {
			if strings.Contains(t.URL, name) {
				nameIndex = j
				break
			}
		}

		score := -2 * nameIndex

		if strings.Contains(t.URL, ".webp") {
			score += 1
		}

		if score > highScore {
			highScore = score
			highIndex = i
		}
	}

	return vid.Thumbnails[highIndex].URL
}

func getYouTubeVideoFast(ctx context.Context, vidURL string) (vid videoInfo, err error) {
	client := youtube.Client{}
	result, err := client.GetVideoContext(ctx, vidURL)
	if err != nil {
		return
	}

	vid = videoInfo{
		title:     result.Title,
		duration:  result.Duration,
		thumbnail: selectThumbnail(result),
	}

	return
}
