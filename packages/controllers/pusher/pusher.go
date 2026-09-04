// Package pusher speaks the Pusher websocket protocol.
//
// It is here because the donation sites all reached for the same thing. Tako
// runs its own relay, Trakteer runs another, and both are Pusher underneath --
// so this is the part that would otherwise be copied once per site, which is
// the part that would then be fixed once per site.
//
// Only what a listener needs: connect, subscribe, keep the socket alive, and
// hand every application event back. Nothing here publishes.
package pusher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// The relays announce activity_timeout: 30 on connecting. Pinging
	// comfortably inside that keeps the socket up through a quiet stream, when
	// an hour can pass with no donations and nothing else to send.
	pingEvery   = 20 * time.Second
	readTimeout = 45 * time.Second
)

// BrowserAgent is what these sites expect to be talking to. Their edge answers
// a request with no browser about it differently, and an overlay is only ever
// opened by one.
const BrowserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

// Options configure one connection.
type Options struct {
	// Endpoint is the whole websocket address, application key and all. The
	// key names the site's software rather than the account -- it is the same
	// for everyone and it is in the page source -- so there is nothing in here
	// worth keeping in secrets.toml.
	Endpoint string

	// Origin is the page the overlay would be served from. The edge in front
	// of these relays answers a request with no browser about it rather
	// differently.
	Origin string

	// Channels are subscribed to in order. They are public: the address is the
	// whole of the authorisation, which is why the overlay link is worth
	// treating as a secret and the application key is not.
	Channels []string

	// OnReady runs once every channel has been accepted, which is the first
	// moment anything can honestly be called connected.
	OnReady func()

	// OnEvent runs for each application event. Housekeeping is dealt with here
	// and never reaches it.
	OnEvent func(channel, event string, data json.RawMessage)
}

// frame is one message in either direction.
//
// Pusher puts the payload in a JSON string rather than nesting it, so Data
// stays raw: an event that cares about its contents unwraps the string and
// decodes it, and plenty of them do not care.
type frame struct {
	Event   string          `json:"event"`
	Channel string          `json:"channel,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Listen holds one connection open until it drops or ctx is cancelled.
//
// It always returns an error. A relay that stopped talking to us is never a
// success, and the caller's job is to decide how long to wait before trying
// again rather than to work out whether anything went wrong.
func Listen(ctx context.Context, options Options) error {
	headers := http.Header{}
	headers.Set("Origin", options.Origin)
	headers.Set("User-Agent", BrowserAgent)

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second

	conn, response, err := dialer.DialContext(ctx, options.Endpoint, headers)
	if err != nil {
		if response != nil {
			return fmt.Errorf("could not reach the relay (%s): %w", response.Status, err)
		}
		return fmt.Errorf("could not reach the relay: %w", err)
	}
	defer conn.Close()

	// One writer, because a websocket connection is not safe for concurrent
	// writes and the ping runs on its own clock.
	var writing sync.Mutex
	send := func(payload any) error {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		writing.Lock()
		defer writing.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.TextMessage, encoded)
	}

	// Closing the connection is what unblocks the read below, so cancellation
	// and shutdown both arrive the same way.
	watching, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	go func() {
		<-watching.Done()
		_ = conn.Close()
	}()

	for _, channel := range options.Channels {
		if err := send(frame{
			Event: "pusher:subscribe",
			Data:  json.RawMessage(fmt.Sprintf(`{"channel":%q}`, channel)),
		}); err != nil {
			return fmt.Errorf("could not subscribe to %s: %w", channel, err)
		}
	}

	go func() {
		ticker := time.NewTicker(pingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-watching.Done():
				return
			case <-ticker.C:
				if err := send(frame{Event: "pusher:ping", Data: json.RawMessage(`{}`)}); err != nil {
					return
				}
			}
		}
	}()

	subscribed := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("the relay stopped sending: %w", err)
		}

		var incoming frame
		if err := json.Unmarshal(raw, &incoming); err != nil {
			slog.Debug("could not understand a relay frame", "frame", string(raw))
			continue
		}

		switch incoming.Event {
		case "pusher:ping":
			_ = send(frame{Event: "pusher:pong", Data: json.RawMessage(`{}`)})
		case "pusher_internal:subscription_succeeded":
			slog.Debug("relay subscribed", "channel", incoming.Channel)
			// Ready means all of them, not the first: a site that listens on a
			// real channel and a test one is only half connected until both
			// have been accepted.
			if subscribed++; subscribed == len(options.Channels) && options.OnReady != nil {
				options.OnReady()
			}
		case "pusher:pong", "pusher:connection_established":
			slog.Debug("relay", "event", incoming.Event)
		case "pusher:error":
			// Returned rather than logged and swallowed: an error here is a
			// rejected subscription or a protocol the relay has moved on from,
			// and neither gets better by reading the next frame.
			return fmt.Errorf("the relay refused the connection: %s", string(incoming.Data))
		default:
			if options.OnEvent != nil {
				options.OnEvent(incoming.Channel, incoming.Event, incoming.Data)
			}
		}
	}
}

// Unwrap decodes an event payload into value.
//
// Pusher sends the payload as a JSON string containing JSON, and not every
// site is consistent about it, so a payload that is already an object is
// accepted too rather than being thrown away as malformed.
func Unwrap(data json.RawMessage, value any) error {
	var nested string
	if err := json.Unmarshal(data, &nested); err == nil {
		return json.Unmarshal([]byte(nested), value)
	}
	return json.Unmarshal(data, value)
}
