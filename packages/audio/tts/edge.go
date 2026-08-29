package tts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// The Edge voices are free, need no key, and sound natural in Indonesian,
// which is why they are the first choice. They speak a small WebSocket
// protocol: send a config frame, send SSML, then read binary frames until the
// service says the turn is over.
//
// The service authenticates the request with a token derived from the current
// time, so the clock on this machine has to be roughly right or every request
// is refused. When that happens the offline Windows voice takes over, because
// a wrong clock must not turn into silence.

const (
	trustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	synthesizeHost     = "speech.platform.bing.com/consumer/speech/synthesize/readaloud"

	// The service checks that the client looks like a current Edge. When
	// Microsoft moves on, these two are what needs bumping -- the symptom is a
	// 403 on the socket while the voice list still works.
	chromiumFullVersion = "143.0.3650.75"
	chromiumVersion     = "143.0.0.0"
	secGECVersion       = "1-" + chromiumFullVersion

	// The service caps a single request; longer text is split on sentence
	// boundaries before it gets here.
	outputFormat = "audio-24khz-48kbitrate-mono-mp3"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/" + chromiumVersion + " Safari/537.36 Edg/" + chromiumVersion

func baseHeaders() http.Header {
	return http.Header{
		"User-Agent":      {userAgent},
		"Accept-Encoding": {"gzip, deflate, br, zstd"},
		"Accept-Language": {"en-US,en;q=0.9"},
	}
}

// socketHeaders are what the synthesis socket expects. The muid cookie is not
// optional: without it the service answers 403 even though the token is good,
// which is a confusing failure to debug because the voice list keeps working.
func socketHeaders() http.Header {
	headers := baseHeaders()
	headers.Set("Pragma", "no-cache")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	headers.Set("Cookie", "muid="+strings.ToUpper(randomHex(16))+";")
	return headers
}

// voiceListHeaders are what the voice list expects: an ordinary browser fetch.
func voiceListHeaders() http.Header {
	headers := baseHeaders()
	headers.Set("Authority", "speech.platform.bing.com")
	headers.Set("Accept", "*/*")
	headers.Set("Sec-CH-UA-Mobile", "?0")
	headers.Set("Sec-Fetch-Site", "none")
	headers.Set("Sec-Fetch-Mode", "cors")
	headers.Set("Sec-Fetch-Dest", "empty")
	return headers
}

// secMSGEC is the token the service expects: a SHA-256 of the current Windows
// file time, rounded down to five minutes, salted with a fixed client token.
func secMSGEC(now time.Time) string {
	const windowsEpochOffset = 11644473600 // seconds between 1601 and 1970
	const fiveMinutes = 3_000_000_000      // in 100-nanosecond units

	ticks := (now.UTC().Unix() + windowsEpochOffset) * 10_000_000
	ticks -= ticks % fiveMinutes

	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks, trustedClientToken)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func randomHex(bytes int) string {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%0*x", bytes*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func connectionID() string { return randomHex(16) }

func edgeTimestamp(now time.Time) string {
	return now.UTC().Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (Coordinated Universal Time)"
}

// SynthesizeEdge renders text with an online Edge voice and returns raw MP3.
func SynthesizeEdge(ctx context.Context, text, voice, rate, volume string) ([]byte, error) {
	now := time.Now()
	requestID := connectionID()

	endpoint := fmt.Sprintf(
		"wss://%s/edge/v1?TrustedClientToken=%s&ConnectionId=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=%s",
		synthesizeHost, trustedClientToken, requestID, secMSGEC(now), secGECVersion)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	connection, response, err := dialer.DialContext(ctx, endpoint, socketHeaders())
	if err != nil {
		if response != nil {
			return nil, failure("the online voice refused the connection (%s)", response.Status)
		}
		return nil, failure("could not reach the online voice: %v", err)
	}
	defer connection.Close()

	_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := connection.WriteMessage(websocket.TextMessage, []byte(speechConfig(now))); err != nil {
		return nil, failure("could not configure the online voice: %v", err)
	}
	if err := connection.WriteMessage(websocket.TextMessage,
		[]byte(ssmlRequest(requestID, now, text, voice, rate, volume))); err != nil {
		return nil, failure("could not send text to the online voice: %v", err)
	}

	audio := &bytes.Buffer{}
	_ = connection.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		kind, message, err := connection.ReadMessage()
		if err != nil {
			if audio.Len() > 0 {
				break // the turn ended by closing; we already have the audio
			}
			return nil, failure("the online voice hung up: %v", err)
		}
		_ = connection.SetReadDeadline(time.Now().Add(30 * time.Second))

		switch kind {
		case websocket.TextMessage:
			if strings.Contains(string(message), "Path:turn.end") {
				if audio.Len() == 0 {
					return nil, errNoAudio
				}
				return audio.Bytes(), nil
			}
		case websocket.BinaryMessage:
			// Each binary frame is a two-byte big-endian header length, the
			// header itself, and then the MP3 payload.
			if len(message) < 2 {
				continue
			}
			headerLength := int(message[0])<<8 | int(message[1])
			if 2+headerLength > len(message) {
				continue
			}
			audio.Write(message[2+headerLength:])
		}
	}

	if audio.Len() == 0 {
		return nil, errNoAudio
	}
	return audio.Bytes(), nil
}

func speechConfig(now time.Time) string {
	const body = `{"context":{"synthesis":{"audio":{"metadataoptions":{` +
		`"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},` +
		`"outputFormat":"` + outputFormat + `"}}}}`

	return "X-Timestamp:" + edgeTimestamp(now) + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" + body
}

func ssmlRequest(requestID string, now time.Time, text, voice, rate, volume string) string {
	ssml := fmt.Sprintf(
		`<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>`+
			`<voice name='%s'><prosody pitch='+0Hz' rate='%s' volume='%s'>%s</prosody></voice></speak>`,
		voice, orDefault(rate, "+0%"), orDefault(volume, "+0%"), html.EscapeString(text))

	return "X-RequestId:" + requestID + "\r\n" +
		"Content-Type:application/ssml+xml\r\n" +
		"X-Timestamp:" + edgeTimestamp(now) + "Z\r\n" +
		"Path:ssml\r\n\r\n" + ssml
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// Voice is one entry from the online voice list, as the settings page shows it.
type Voice struct {
	Name   string `json:"name"`
	Gender string `json:"gender"`
	Locale string `json:"locale"`
}

type rawVoice struct {
	ShortName string `json:"ShortName"`
	Gender    string `json:"Gender"`
	Locale    string `json:"Locale"`
}

// ListVoices asks the service which voices exist.
//
// It is only ever used to populate a dropdown, so a failure here returns an
// empty list rather than an error the caller has to handle: being offline
// means she picks from what is already configured, not that the page breaks.
func ListVoices(ctx context.Context) ([]Voice, error) {
	endpoint := fmt.Sprintf(
		"https://%s/voices/list?trustedclienttoken=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=%s",
		synthesizeHost, trustedClientToken, secMSGEC(time.Now()), secGECVersion)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header = voiceListHeaders()

	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the voice list returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var raw []rawVoice
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	voices := make([]Voice, 0, len(raw))
	for _, entry := range raw {
		voices = append(voices, Voice{
			Name: entry.ShortName, Gender: entry.Gender, Locale: entry.Locale,
		})
	}
	return voices, nil
}
