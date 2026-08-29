// Package youtube covers OAuth, broadcast metadata, and quota accounting.
//
// The consent screen is a browser flow and genuinely cannot be driven by
// voice. It is the one setup step that needs sighted or screen-reader help.
// Everything after it is voice-operable, because the refresh token is cached
// and reused.
package youtube

import (
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
	"golang.org/x/oauth2/google"
	googleapi "google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Scope covers reading and writing broadcasts as well as live chat, so one
// consent screen serves every feature rather than asking her twice.
const Scope = yt.YoutubeForceSslScope

// Error is a YouTube failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// NotAuthenticatedError means the account has not been connected yet.
type NotAuthenticatedError struct{ Reason string }

func (e *NotAuthenticatedError) Error() string { return e.Reason }

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

// Controller talks to the YouTube Data API.
type Controller struct {
	mu           sync.RWMutex
	service      *yt.Service
	tokenSource  oauth2.TokenSource
	channelTitle string
	broadcast    *Broadcast

	Quota *Ledger
}

// New builds a controller with its own quota ledger.
func New(budget, warnPercent int) *Controller {
	return &Controller{Quota: NewLedger(budget, warnPercent)}
}

// -- authentication -----------------------------------------------------------

// Authenticated reports whether the API can be called.
func (c *Controller) Authenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.service != nil
}

// HasClientSecret reports whether the OAuth client file has been downloaded.
func HasClientSecret() bool {
	_, err := os.Stat(paths.ClientSecretFile())
	return err == nil
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

// clientSecret is the OAuth desktop client downloaded from Google Cloud.
func oauthConfig() (*oauth2.Config, error) {
	data, err := os.ReadFile(paths.ClientSecretFile())
	if err != nil {
		return nil, &NotAuthenticatedError{Reason: fmt.Sprintf(
			"missing %s; download OAuth desktop credentials from Google Cloud and save them there",
			"data/client_secret.json")}
	}
	settings, err := google.ConfigFromJSON(data, Scope)
	if err != nil {
		return nil, &NotAuthenticatedError{
			Reason: "data/client_secret.json could not be read: " + err.Error()}
	}
	return settings, nil
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

// classify turns a Google API failure into the right error type, so an
// exhausted quota is handled as such rather than as a generic outage.
func (c *Controller) classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, detail := range apiErr.Errors {
			if detail.Reason == "quotaExceeded" || detail.Reason == "rateLimitExceeded" {
				c.Quota.MarkExhausted()
				return &QuotaExhaustedError{Reason: err.Error()}
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
func (c *Controller) ActiveBroadcast(ctx context.Context, refresh bool) (*Broadcast, error) {
	if !refresh {
		c.mu.RLock()
		cached := c.broadcast
		c.mu.RUnlock()
		if cached != nil {
			return cached, nil
		}
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
		c.broadcast = found
		c.mu.Unlock()
		return found, nil
	}

	c.mu.Lock()
	c.broadcast = nil
	c.mu.Unlock()
	return nil, nil
}

// ViewerCount is how many people are watching right now.
func (c *Controller) ViewerCount(ctx context.Context) (int, error) {
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
