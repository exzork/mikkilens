package youtube

import (
	"context"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/option"
	yt "google.golang.org/api/youtube/v3"
)

// Reading a public stream needs only an API key.
//
// The consent screen is the one setup step that genuinely cannot be done by
// voice, and it is the step people give up on: a Google Cloud project, an
// OAuth client of the right type, a JSON file in the right place, and a
// browser page someone else has to read out. An API key is one string copied
// from one page.
//
// It buys the readable half of what she asks for -- how many people are
// watching, what the stream is called -- because a public live stream is
// public. Changing the title still needs the account, because that is a write
// to her channel, and no key should be able to do that.
//
// The stream key OBS uses is not an alternative here. RTMP is a one-way media
// upload and carries nothing back; OBS's own viewer count and chat dock come
// from this same API, signed in.

// publicRefresh is how long a resolved video id is trusted before looking
// again. Finding the current live video costs a hundred quota units, against
// one unit to read it, so it is worth not repeating.
const publicRefresh = 5 * time.Minute

var (
	videoIDPattern   = regexp.MustCompile(`(?:v=|youtu\.be/|/live/|/shorts/)([A-Za-z0-9_-]{11})`)
	bareVideoID      = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	channelIDPattern = regexp.MustCompile(`(UC[A-Za-z0-9_-]{22})`)
)

// ParseVideoID pulls a video id out of whatever she pasted: a watch URL, a
// youtu.be link, a /live/ link, or the id on its own.
func ParseVideoID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if bareVideoID.MatchString(value) {
		return value
	}
	if found := videoIDPattern.FindStringSubmatch(value); found != nil {
		return found[1]
	}
	return ""
}

// ParseChannelID pulls a channel id out of a URL or returns it unchanged.
//
// Only the UC... form is usable directly. A handle like @name would need a
// lookup of its own, which is reported rather than guessed at.
func ParseChannelID(value string) string {
	value = strings.TrimSpace(value)
	if found := channelIDPattern.FindString(value); found != "" {
		return found
	}
	return ""
}

// usePublic reports whether an API key is configured.
func (c *Controller) usePublic() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.publicService != nil
}

// startPublic builds the read-only client. It is not an error to have no key;
// that simply means this path is unavailable.
func (c *Controller) startPublic(ctx context.Context) error {
	c.mu.RLock()
	key := c.apiKey
	c.mu.RUnlock()

	if key == "" {
		return nil
	}
	service, err := yt.NewService(ctx, option.WithAPIKey(key))
	if err != nil {
		return &Error{Reason: "the YouTube API key could not be used: " + err.Error()}
	}

	c.mu.Lock()
	c.publicService = service
	c.mu.Unlock()
	return nil
}

// publicBroadcast reads the current stream with the API key alone.
func (c *Controller) publicBroadcast(ctx context.Context, refresh bool) (*Broadcast, error) {
	c.mu.RLock()
	service := c.publicService
	cached := c.broadcast
	resolvedAt := c.publicResolved
	c.mu.RUnlock()

	if service == nil {
		return nil, &NotAuthenticatedError{Reason: "YouTube is not connected"}
	}
	if c.Quota.Exhausted() {
		return nil, &QuotaExhaustedError{Reason: "the daily YouTube quota is used up"}
	}

	videoID, err := c.publicVideoID(ctx, refresh, cached, resolvedAt)
	if err != nil {
		return nil, err
	}
	if videoID == "" {
		return nil, nil
	}

	response, err := service.Videos.
		List([]string{"snippet", "liveStreamingDetails"}).
		Id(videoID).
		Context(ctx).Do()
	c.Quota.Spend("videos.list")
	if err != nil {
		return nil, c.classify(err)
	}
	if len(response.Items) == 0 {
		// The stream ended, or the id is wrong. Either way, look again next
		// time rather than holding a video that no longer exists.
		c.mu.Lock()
		c.broadcast, c.broadcastFetched = nil, time.Time{}
		c.publicResolved = time.Time{}
		c.mu.Unlock()
		return nil, nil
	}

	item := response.Items[0]
	found := &Broadcast{ID: item.Id, Status: "active"}
	if item.Snippet != nil {
		found.Title = item.Snippet.Title
	}
	if details := item.LiveStreamingDetails; details != nil {
		found.LiveChatID = details.ActiveLiveChatId
		found.Viewers = int(details.ConcurrentViewers)
		if details.ActualEndTime != "" {
			found.Status = "complete"
		}
	}

	c.mu.Lock()
	c.broadcast, c.broadcastFetched = found, time.Now()
	c.mu.Unlock()
	return found, nil
}

// publicVideoID finds which video to read.
//
// A pinned video id is used as-is. Otherwise the channel is searched for
// whatever is live, and the answer is kept for a while: the search costs a
// hundred units against one to read the video, and a long stream would
// otherwise spend the whole day's quota looking up a video that has not
// changed.
func (c *Controller) publicVideoID(ctx context.Context, refresh bool, cached *Broadcast, resolvedAt time.Time) (string, error) {
	c.mu.RLock()
	pinned, channelID, service := c.videoID, c.channelID, c.publicService
	c.mu.RUnlock()

	if pinned != "" {
		return pinned, nil
	}
	if !refresh && cached != nil && time.Since(resolvedAt) < publicRefresh {
		return cached.ID, nil
	}
	if channelID == "" {
		return "", &Error{Reason: "no channel to watch: put your channel id or the " +
			"link to your stream in the settings"}
	}

	response, err := service.Search.
		List([]string{"id"}).
		ChannelId(channelID).
		EventType("live").
		Type("video").
		MaxResults(1).
		Context(ctx).Do()
	c.Quota.Spend("search.list")
	if err != nil {
		return "", c.classify(err)
	}
	if len(response.Items) == 0 || response.Items[0].Id == nil {
		return "", nil
	}

	c.mu.Lock()
	c.publicResolved = time.Now()
	c.mu.Unlock()
	return response.Items[0].Id.VideoId, nil
}

// publicChatMessages reads live chat with the API key.
//
// Google has tightened this over time, so a refusal here is expected rather
// than exceptional: it comes back as something worth saying aloud instead of
// an HTTP code.
func (c *Controller) publicChatMessages(ctx context.Context, liveChatID, pageToken string) (*yt.LiveChatMessageListResponse, error) {
	c.mu.RLock()
	service := c.publicService
	c.mu.RUnlock()

	if service == nil {
		return nil, &NotAuthenticatedError{Reason: "YouTube is not connected"}
	}
	if c.Quota.Exhausted() {
		return nil, &QuotaExhaustedError{Reason: "the daily YouTube quota is used up"}
	}

	call := service.LiveChatMessages.
		List(liveChatID, []string{"id", "snippet", "authorDetails"}).
		MaxResults(200).
		Context(ctx)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}

	response, err := call.Do()
	c.Quota.Spend("liveChatMessages.list")
	if err != nil {
		if isForbidden(err) {
			return nil, &Error{Reason: "reading chat needs you to sign in to YouTube; " +
				"the viewer count and the title work with just the key"}
		}
		return nil, c.classify(err)
	}
	return response, nil
}

func isForbidden(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "403") ||
		strings.Contains(text, "forbidden") ||
		strings.Contains(text, "insufficient")
}
