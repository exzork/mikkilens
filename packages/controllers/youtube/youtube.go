// Package youtube covers OAuth, broadcast metadata, and quota accounting.
//
// The consent screen is a browser flow and genuinely cannot be driven by
// voice. It is the one setup step that needs sighted or screen-reader help.
// Everything after it is voice-operable, because the refresh token is cached
// and reused.
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	googleapi "google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// Scope is the narrowest one that covers what MikkiLens actually does.
//
// The obvious choice is youtube.force-ssl, and it is the wrong one. Google
// reads it out on the consent screen as "see, edit, and permanently delete
// your YouTube videos, ratings, comments, and captions" -- which is alarming,
// accurate, and far more than this application ever asks for. She has to have
// that sentence read to her by whoever is helping, and being asked to hand
// over deletion rights to her own channel is a good reason to say no.
//
// Every call MikkiLens makes is satisfied by plain youtube, which Google
// describes as "manage your YouTube account":
//
//	liveBroadcasts.list       read
//	liveBroadcasts.update     write -- the only one, and only the title
//	liveChatMessages.list     read
//	liveChat.messages.stream  read
//	videos.list               read
//	channels.list             read
//	search.list               read
//
// Everything but liveBroadcasts.update would also be satisfied by
// youtube.readonly, so changing the title by voice is the single feature that
// costs a write scope at all.
//
// Adding a feature that posts to chat (liveChatMessages.insert) would drag
// force-ssl back in. That is a real cost to weigh, not a detail: it would
// change what she is asked to agree to.
const Scope = yt.YoutubeScope

// Error is a YouTube failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// NotAuthenticatedError means the account has not been connected yet.
type NotAuthenticatedError struct{ Reason string }

func (e *NotAuthenticatedError) Error() string { return e.Reason }

// ExpiredCredentialsError means the saved consent is no longer accepted and
// she has to sign in again.
//
// This is worth its own type rather than folding into "not connected", because
// the two need different answers. Not connected means she never signed in;
// this means she did, it worked, and it stopped -- and the only thing that
// fixes it is pressing the button again.
type ExpiredCredentialsError struct{ Reason string }

func (e *ExpiredCredentialsError) Error() string { return e.Reason }

// ChatUnavailableError means this broadcast has no live chat to read.
//
// Chat can be switched off for a broadcast, and it ends when the stream ends.
// Neither will start working by trying again, so this is separated from an
// ordinary failure: retrying it forever is what turns one problem into a
// stream of announcements she cannot switch off.
type ChatUnavailableError struct{ Reason string }

func (e *ChatUnavailableError) Error() string { return e.Reason }

// RateLimitedError means Google is being asked too quickly, right now.
//
// It is emphatically not the daily quota running out, and conflating the two
// was a real fault: a burst of requests would mark the whole day spent and
// leave her without chat or viewer counts until midnight, with the ledger
// insisting the allowance was gone when almost none of it had been used.
type RateLimitedError struct{ Reason string }

func (e *RateLimitedError) Error() string { return e.Reason }

// QuotaExhaustedError means the daily allowance is spent.
type QuotaExhaustedError struct{ Reason string }

func (e *QuotaExhaustedError) Error() string { return e.Reason }

// Broadcast is the stream she is running, or the next one due to start.
type Broadcast struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	LiveChatID string `json:"live_chat_id"`
	Viewers    int    `json:"viewers"`
}

// Controller talks to the YouTube Data API, signed in or with a key.
type Controller struct {
	mu           sync.RWMutex
	service      *yt.Service
	tokenSource  oauth2.TokenSource
	channelTitle string
	broadcast    *Broadcast

	// When the cached broadcast was fetched. Without this the first answer is
	// kept for the life of the process, which is wrong for everything on it:
	// the title she changed in Studio, whether chat exists, and the broadcast
	// id itself once she starts a new stream.
	broadcastFetched time.Time

	// The read-only path: an API key, a channel to watch, and when the live
	// video was last looked up.
	publicService  *yt.Service
	apiKey         string
	channelID      string
	videoID        string
	publicResolved time.Time

	Quota *Ledger
}

// New builds a controller from the YouTube settings.
func New(settings config.YouTube, apiKey string) *Controller {
	return &Controller{
		Quota:     NewLedger(settings.QuotaBudget, settings.QuotaWarnPercent),
		apiKey:    apiKey,
		channelID: ParseChannelID(settings.ChannelID),
		videoID:   ParseVideoID(settings.VideoID),
	}
}

// Access describes how much MikkiLens can currently do.
type Access string

const (
	// AccessNone: neither a sign-in nor a key.
	AccessNone Access = "none"
	// AccessPublic: an API key. Reading works, writing does not.
	AccessPublic Access = "public"
	// AccessAccount: signed in. Everything works.
	AccessAccount Access = "account"
)

// Access reports which of the two ways in is available.
func (c *Controller) Access() Access {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch {
	case c.service != nil:
		return AccessAccount
	case c.publicService != nil:
		return AccessPublic
	default:
		return AccessNone
	}
}

// Available reports whether anything can be read at all.
func (c *Controller) Available() bool { return c.Access() != AccessNone }

// StartPublic prepares the key-only path. It is not an error to have no key.
func (c *Controller) StartPublic(ctx context.Context) error { return c.startPublic(ctx) }

// -- authentication -----------------------------------------------------------

// Authenticated reports whether the API can be called.
func (c *Controller) Authenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.service != nil
}

// HasToken reports whether a previous consent has been cached.
func HasToken() bool {
	_, err := os.Stat(paths.TokenFile())
	return err == nil
}

// LoadSavedCredentials reuses a cached token.
//
// It opens no browser, so it is safe to run at startup: the consent screen
// must never appear unasked in the middle of a stream.
func (c *Controller) LoadSavedCredentials(ctx context.Context) (bool, error) {
	settings, err := oauthConfig()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(paths.TokenFile())
	if err != nil {
		return false, nil
	}

	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		slog.Warn("the stored YouTube token is unusable", "error", err)
		return false, nil
	}
	if token.RefreshToken == "" && !token.Valid() {
		return false, nil
	}

	source := settings.TokenSource(ctx, &token)
	refreshed, err := source.Token()
	if err != nil {
		if isRevokedGrant(err) {
			// The consent is gone for good, so the stored token is dead weight:
			// keeping it would fail identically at every start. Removing it
			// puts things back to "never signed in", which is true and is a
			// state the settings page already knows how to describe.
			//
			// A Google Cloud project still in Testing expires refresh tokens
			// after seven days, so for such a build this is not an edge case;
			// it is next week.
			if err := os.Remove(paths.TokenFile()); err != nil && !os.IsNotExist(err) {
				slog.Warn("could not remove the dead YouTube token", "error", err)
			}
			return false, &ExpiredCredentialsError{Reason: "your YouTube sign-in has " +
				"expired; open the settings app and connect YouTube again"}
		}
		// Anything else is probably the network, and will likely work later.
		slog.Warn("could not refresh the YouTube token", "error", err)
		return false, nil
	}
	if refreshed.AccessToken != token.AccessToken {
		c.writeToken(refreshed)
	}

	service, err := yt.NewService(ctx, option.WithTokenSource(source))
	if err != nil {
		return false, &Error{Reason: err.Error()}
	}

	c.mu.Lock()
	c.service, c.tokenSource = service, source
	c.mu.Unlock()
	return true, nil
}

// Authorize runs the browser consent flow and blocks until she finishes or
// cancels. This is the step that needs help once, and only once.
func (c *Controller) Authorize(ctx context.Context, openBrowser func(url string)) error {
	settings, err := oauthConfig()
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return &Error{Reason: "could not open a port for the sign-in: " + err.Error()}
	}
	defer listener.Close()

	settings.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	state := fmt.Sprintf("mikkilens-%d", time.Now().UnixNano())

	type result struct {
		code string
		err  error
	}
	answers := make(chan result, 1)

	server := &http.Server{Handler: http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			query := request.URL.Query()
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")

			if failure := query.Get("error"); failure != "" {
				_, _ = writer.Write([]byte(consentPage("Sign-in was cancelled.")))
				answers <- result{err: &Error{Reason: "sign-in was cancelled: " + failure}}
				return
			}
			if query.Get("state") != state {
				_, _ = writer.Write([]byte(consentPage("That sign-in did not match this request.")))
				answers <- result{err: &Error{Reason: "the sign-in response did not match"}}
				return
			}
			_, _ = writer.Write([]byte(consentPage(
				"YouTube is connected. You can close this tab and go back to MikkiLens.")))
			answers <- result{code: query.Get("code")}
		})}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	url := settings.AuthCodeURL(state,
		oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	if openBrowser != nil {
		openBrowser(url)
	}

	timed, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var answer result
	select {
	case answer = <-answers:
	case <-timed.Done():
		return &Error{Reason: "the sign-in timed out"}
	}
	if answer.err != nil {
		return answer.err
	}

	token, err := settings.Exchange(ctx, answer.code)
	if err != nil {
		return &Error{Reason: "the sign-in could not be completed: " + err.Error()}
	}
	c.writeToken(token)

	source := settings.TokenSource(ctx, token)
	service, err := yt.NewService(ctx, option.WithTokenSource(source))
	if err != nil {
		return &Error{Reason: err.Error()}
	}

	c.mu.Lock()
	c.service, c.tokenSource = service, source
	c.mu.Unlock()
	return nil
}

func consentPage(message string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<title>MikkiLens</title></head><body>` +
		`<h1>MikkiLens</h1><p role="status">` + message + `</p></body></html>`
}

// SignOut forgets the account.
func (c *Controller) SignOut() {
	c.mu.Lock()
	c.service, c.tokenSource = nil, nil
	c.channelTitle, c.broadcast = "", nil
	c.mu.Unlock()
	_ = os.Remove(paths.TokenFile())
}

// Token is the current access token, which the streaming chat transport needs
// to make its own request.
func (c *Controller) Token() (*oauth2.Token, error) {
	c.mu.RLock()
	source := c.tokenSource
	c.mu.RUnlock()

	if source == nil {
		return nil, &NotAuthenticatedError{Reason: "YouTube is not connected"}
	}
	return source.Token()
}

func (c *Controller) writeToken(token *oauth2.Token) {
	if _, err := paths.EnsureDataDir(); err != nil {
		return
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		return
	}
	// The token is a credential, so it is written for this user only.
	if err := os.WriteFile(paths.TokenFile(), encoded, 0o600); err != nil {
		slog.Warn("could not save the YouTube token", "error", err)
	}
}

// -- request plumbing ---------------------------------------------------------

// service returns the API client, refusing before the network is touched when
// the quota is already gone.
func (c *Controller) apiService(method string) (*yt.Service, error) {
	c.mu.RLock()
	service := c.service
	c.mu.RUnlock()

	if service == nil {
		return nil, &NotAuthenticatedError{Reason: "YouTube is not connected"}
	}
	if c.Quota.Exhausted() {
		return nil, &QuotaExhaustedError{Reason: "the daily YouTube quota is used up"}
	}
	return service, nil
}

// AuthorizeStream prepares a raw streaming request.
//
// Live chat streaming is a long-lived response that the generated client
// cannot express -- it decodes one document and returns -- so the request is
// built by hand and authorized here, where both ways in are known. Signed in
// it carries a bearer token; with a key alone it carries the key.
func (c *Controller) AuthorizeStream(request *http.Request) error {
	c.mu.RLock()
	source, key := c.tokenSource, c.apiKey
	c.mu.RUnlock()

	if source != nil {
		// Fetched per connection, not once per stream: an access token lasts
		// about an hour, and a stream can easily run longer than that.
		token, err := source.Token()
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token.AccessToken)
		return nil
	}

	if key != "" {
		query := request.URL.Query()
		query.Set("key", key)
		request.URL.RawQuery = query.Encode()
		return nil
	}
	return &NotAuthenticatedError{Reason: "YouTube is not connected"}
}

// isRevokedGrant reports whether Google has refused the refresh token for
// good, as opposed to failing for a reason that might clear up.
//
// "invalid_grant" is what a revoked, expired or already-used refresh token
// comes back as. It is checked as a string as well as a typed field because
// the token endpoint's shape has changed over the years and getting this wrong
// means silently retrying a credential that will never work again.
func isRevokedGrant(err error) bool {
	if err == nil {
		return false
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) && retrieve.ErrorCode == "invalid_grant" {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "invalid_grant")
}

// ClassifyHTTP turns a raw error response into the right error type.
//
// The streaming endpoint is called by hand rather than through the generated
// client, so its failures arrive as a status code and a JSON body instead of
// a typed error. Without this, "live chat is switched off for this broadcast"
// would be indistinguishable from a network blip and retried forever.
func (c *Controller) ClassifyHTTP(status int, body []byte, fallback error) error {
	type envelope struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}

	// The streaming endpoint answers with a JSON *array* of documents, so its
	// errors arrive as [{"error":...}] where every other endpoint sends
	// {"error":...}. Parsing only the object shape meant every streaming
	// failure fell through unclassified and got retried as if temporary.
	var payload envelope
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []envelope
		if err := json.Unmarshal(trimmed, &batch); err != nil || len(batch) == 0 {
			return fallback
		}
		payload = batch[0]
	} else if err := json.Unmarshal(body, &payload); err != nil {
		return fallback
	}

	message := payload.Error.Message
	if message == "" {
		message = fallback.Error()
	}
	for _, detail := range payload.Error.Errors {
		switch detail.Reason {
		case "liveChatDisabled", "liveChatEnded", "liveChatNotFound", "liveChatUserBanned":
			return &ChatUnavailableError{Reason: message}
		case "quotaExceeded", "dailyLimitExceeded":
			c.Quota.MarkExhausted()
			return &QuotaExhaustedError{Reason: message}
		case "rateLimitExceeded", "userRateLimitExceeded":
			return &RateLimitedError{Reason: message}
		case "authError", "unauthorized":
			return &NotAuthenticatedError{Reason: message}
		}
	}
	if status == http.StatusForbidden {
		// A forbidden stream with no reason we recognise is still not
		// something that retrying will fix.
		return &ChatUnavailableError{Reason: message}
	}
	return fallback
}

// classify turns a Google API failure into the right error type, so an
// exhausted quota is handled as such rather than as a generic outage.
func (c *Controller) classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, detail := range apiErr.Errors {
			switch detail.Reason {
			case "quotaExceeded", "dailyLimitExceeded":
				c.Quota.MarkExhausted()
				return &QuotaExhaustedError{Reason: err.Error()}
			case "rateLimitExceeded", "userRateLimitExceeded":
				// Asking too fast for a moment, which clears in seconds.
				// Treating it as the daily allowance being gone would switch
				// YouTube off until midnight over a burst of requests -- and
				// that is exactly what a retry loop provokes.
				return &RateLimitedError{Reason: err.Error()}
			}
		}
	}
	if errors.As(err, &apiErr) {
		for _, detail := range apiErr.Errors {
			switch detail.Reason {
			case "liveChatDisabled", "liveChatEnded", "liveChatNotFound",
				"forbidden", "liveChatUserBanned":
				return &ChatUnavailableError{Reason: err.Error()}
			}
		}
	}
	if strings.Contains(err.Error(), "quotaExceeded") {
		c.Quota.MarkExhausted()
		return &QuotaExhaustedError{Reason: err.Error()}
	}
	return &Error{Reason: err.Error()}
}

// -- broadcasts ---------------------------------------------------------------

// ActiveBroadcast is the broadcast currently live, or the next one due.
// broadcastTTL is how long a fetched broadcast is trusted.
//
// liveBroadcasts.list costs one quota unit, so re-asking is close to free, and
// the cost of not re-asking is high: reading out a title she has since
// changed, or holding a dead broadcast after she ends one stream and starts
// another. A minute is short enough that nothing stays wrong for long.
const broadcastTTL = time.Minute

func (c *Controller) ActiveBroadcast(ctx context.Context, refresh bool) (*Broadcast, error) {
	if !refresh {
		if cached, fresh := c.cachedBroadcast(); fresh {
			return cached, nil
		}
	}

	// Without a sign-in, read the public video instead. It answers the two
	// questions she actually asks -- how many are watching, what is it called.
	if c.Access() == AccessPublic {
		return c.publicBroadcast(ctx, refresh)
	}

	service, err := c.apiService("liveBroadcasts.list")
	if err != nil {
		return nil, err
	}

	for _, status := range []string{"active", "upcoming"} {
		response, err := service.LiveBroadcasts.
			List([]string{"id", "snippet", "status"}).
			BroadcastStatus(status).
			MaxResults(1).
			Context(ctx).Do()
		c.Quota.Spend("liveBroadcasts.list")
		if err != nil {
			return nil, c.classify(err)
		}
		if len(response.Items) == 0 {
			continue
		}

		item := response.Items[0]
		found := &Broadcast{ID: item.Id, Status: status}
		if item.Snippet != nil {
			found.Title = item.Snippet.Title
			found.LiveChatID = item.Snippet.LiveChatId
		}
		if item.Status != nil && item.Status.LifeCycleStatus != "" {
			found.Status = item.Status.LifeCycleStatus
		}

		c.mu.Lock()
		c.broadcast, c.broadcastFetched = found, time.Now()
		c.mu.Unlock()
		return found, nil
	}

	c.mu.Lock()
	c.broadcast, c.broadcastFetched = nil, time.Time{}
	c.mu.Unlock()
	return nil, nil
}

// cachedBroadcast returns the cached broadcast and whether it is still fresh.
func (c *Controller) cachedBroadcast() (*Broadcast, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.broadcast == nil || c.broadcastFetched.IsZero() {
		return c.broadcast, false
	}
	return c.broadcast, time.Since(c.broadcastFetched) < broadcastTTL
}

// InvalidateBroadcast forgets the cached broadcast.
//
// Used when something has just proved the cached answer wrong -- reading chat
// failed because this broadcast has none -- so that the next look genuinely
// asks YouTube instead of handing back the same stale answer and failing
// identically forever.
func (c *Controller) InvalidateBroadcast() {
	c.mu.Lock()
	c.broadcast, c.broadcastFetched = nil, time.Time{}
	c.publicResolved = time.Time{}
	c.mu.Unlock()
}

// ViewerCount is how many people are watching right now.
func (c *Controller) ViewerCount(ctx context.Context) (int, error) {
	// The public read returns the count with the broadcast, so there is
	// nothing further to ask for.
	if c.Access() == AccessPublic {
		broadcast, err := c.publicBroadcast(ctx, true)
		if err != nil {
			return 0, err
		}
		if broadcast == nil {
			return 0, &Error{Reason: "there is no active broadcast"}
		}
		return broadcast.Viewers, nil
	}

	broadcast, err := c.currentBroadcast(ctx)
	if err != nil {
		return 0, err
	}

	service, err := c.apiService("videos.list")
	if err != nil {
		return 0, err
	}
	response, err := service.Videos.
		List([]string{"liveStreamingDetails"}).
		Id(broadcast.ID).
		Context(ctx).Do()
	c.Quota.Spend("videos.list")
	if err != nil {
		return 0, c.classify(err)
	}
	if len(response.Items) == 0 || response.Items[0].LiveStreamingDetails == nil {
		return 0, nil
	}

	count := int(response.Items[0].LiveStreamingDetails.ConcurrentViewers)
	c.mu.Lock()
	if c.broadcast != nil {
		c.broadcast.Viewers = count
	}
	c.mu.Unlock()
	return count, nil
}

// Title is the current broadcast title.
func (c *Controller) Title(ctx context.Context) (string, error) {
	broadcast, err := c.ActiveBroadcast(ctx, true)
	if err != nil {
		return "", err
	}
	if broadcast == nil {
		return "", &Error{Reason: "there is no active broadcast"}
	}
	return broadcast.Title, nil
}

// SetTitle renames the broadcast.
func (c *Controller) SetTitle(ctx context.Context, title string) error {
	if c.Access() == AccessPublic {
		// A key can read a public stream; changing her channel is a write, and
		// no key should be able to do that.
		return &NotAuthenticatedError{Reason: "changing the title needs you to " +
			"sign in to YouTube; the viewer count and the title work with just the key"}
	}

	broadcast, err := c.currentBroadcast(ctx)
	if err != nil {
		return err
	}
	service, err := c.apiService("liveBroadcasts.update")
	if err != nil {
		return err
	}

	_, err = service.LiveBroadcasts.
		Update([]string{"id", "snippet"}, &yt.LiveBroadcast{
			Id:      broadcast.ID,
			Snippet: &yt.LiveBroadcastSnippet{Title: title},
		}).
		Context(ctx).Do()
	c.Quota.Spend("liveBroadcasts.update")
	if err != nil {
		return c.classify(err)
	}

	c.mu.Lock()
	if c.broadcast != nil {
		c.broadcast.Title = title
	}
	c.mu.Unlock()
	return nil
}

// LiveChatID is the chat attached to the current broadcast.
func (c *Controller) LiveChatID(ctx context.Context) (string, error) {
	broadcast, err := c.currentBroadcast(ctx)
	if err != nil {
		return "", err
	}
	if broadcast.LiveChatID == "" {
		return "", &Error{Reason: "there is no live chat for the current broadcast"}
	}
	return broadcast.LiveChatID, nil
}

// ChannelName is her channel's name, cached after the first look.
func (c *Controller) ChannelName(ctx context.Context) (string, error) {
	c.mu.RLock()
	cached := c.channelTitle
	c.mu.RUnlock()
	if cached != "" {
		return cached, nil
	}

	if c.Access() == AccessPublic {
		// Mine(true) needs a sign-in. The channel is named by its id here,
		// which is at least something to say.
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.channelID, nil
	}

	service, err := c.apiService("channels.list")
	if err != nil {
		return "", err
	}
	response, err := service.Channels.List([]string{"snippet"}).Mine(true).Context(ctx).Do()
	c.Quota.Spend("channels.list")
	if err != nil {
		return "", c.classify(err)
	}
	if len(response.Items) == 0 || response.Items[0].Snippet == nil {
		return "", nil
	}

	title := response.Items[0].Snippet.Title
	c.mu.Lock()
	c.channelTitle = title
	c.mu.Unlock()
	return title, nil
}

// ChannelTitle is whatever name has already been looked up, without a call.
func (c *Controller) ChannelTitle() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channelTitle
}

// currentBroadcast prefers the cached broadcast and looks one up if there is
// none, which keeps a read that happens every few seconds off the quota.
func (c *Controller) currentBroadcast(ctx context.Context) (*Broadcast, error) {
	broadcast, err := c.ActiveBroadcast(ctx, false)
	if err != nil {
		return nil, err
	}
	if broadcast == nil {
		if broadcast, err = c.ActiveBroadcast(ctx, true); err != nil {
			return nil, err
		}
	}
	if broadcast == nil {
		return nil, &Error{Reason: "there is no active broadcast"}
	}
	return broadcast, nil
}

// ListChatMessages fetches one page of live chat, spending its quota. The
// polling transport calls it; the streaming one goes over raw HTTP.
func (c *Controller) ListChatMessages(ctx context.Context, liveChatID, pageToken string) (*yt.LiveChatMessageListResponse, error) {
	if c.Access() == AccessPublic {
		return c.publicChatMessages(ctx, liveChatID, pageToken)
	}

	service, err := c.apiService("liveChatMessages.list")
	if err != nil {
		return nil, err
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
		return nil, c.classify(err)
	}
	return response, nil
}
