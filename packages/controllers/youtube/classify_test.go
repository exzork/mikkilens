package youtube

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// The streaming endpoint is called by hand, so its failures arrive as a status
// code and a JSON body rather than a typed error. Getting this wrong is what
// made "live chat is switched off" look like a network blip and get retried
// every two seconds, forever, out loud.

const chatDisabledBody = `{"error":{"code":403,` +
	`"message":"Live chat is not enabled for the specified broadcast.",` +
	`"errors":[{"message":"Live chat is not enabled for the specified broadcast.",` +
	`"domain":"youtube.liveChat","reason":"liveChatDisabled"}]}}`

func TestChatDisabledIsRecognisedFromARawStreamingResponse(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}

	err := controller.ClassifyHTTP(http.StatusForbidden, []byte(chatDisabledBody),
		errors.New("streamList returned 403 Forbidden"))

	var unavailable *ChatUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error is %T, want ChatUnavailableError", err)
	}
	// The reason is spoken aloud, so it has to be the sentence a person can
	// act on rather than the HTTP status line.
	if unavailable.Reason != "Live chat is not enabled for the specified broadcast." {
		t.Errorf("reason is %q", unavailable.Reason)
	}
}

func TestAnExhaustedQuotaIsRecognisedFromARawStreamingResponse(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := `{"error":{"code":403,"message":"quota exceeded",` +
		`"errors":[{"reason":"quotaExceeded"}]}}`

	err := controller.ClassifyHTTP(http.StatusForbidden, []byte(body),
		errors.New("streamList returned 403"))

	if _, ok := err.(*QuotaExhaustedError); !ok {
		t.Fatalf("error is %T, want QuotaExhaustedError", err)
	}
	// Marking it locally is what stops the next call going out at all.
	if !controller.Quota.Exhausted() {
		t.Error("the ledger must record that the quota is gone")
	}
}

// A body that is not the shape we expect must fall through unchanged rather
// than being guessed at -- a wrong guess here either retries something that
// will never work, or gives up on something that would have.
func TestAnUnrecognisableBodyKeepsTheOriginalError(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	fallback := errors.New("streamList returned 500 Internal Server Error")

	err := controller.ClassifyHTTP(http.StatusInternalServerError,
		[]byte("<html>gateway error</html>"), fallback)

	if err != fallback {
		t.Errorf("error is %v, want the original passed through", err)
	}
	var unavailable *ChatUnavailableError
	if errors.As(err, &unavailable) {
		t.Error("a server error must stay retryable, not become permanent")
	}
}

// A 500 is temporary and must keep being retried; a 403 will not fix itself.
func TestAServerErrorStaysRetryableButForbiddenDoesNot(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := []byte(`{"error":{"code":403,"message":"Forbidden"}}`)

	var unavailable *ChatUnavailableError
	if err := controller.ClassifyHTTP(http.StatusForbidden, body,
		errors.New("403")); !errors.As(err, &unavailable) {
		t.Errorf("a forbidden stream is %T, want ChatUnavailableError", err)
	}

	temporary := errors.New("503")
	if err := controller.ClassifyHTTP(http.StatusServiceUnavailable,
		[]byte(`{"error":{"code":503,"message":"try later"}}`), temporary); err != temporary {
		t.Errorf("a 503 is %v, want it left retryable", err)
	}
}

// The streaming endpoint answers with a JSON array of documents, so its errors
// arrive wrapped in one. This is the exact body observed from a live 403.
const streamingArrayBody = `[{
  "error": {
    "code": 403,
    "message": "Live chat is not enabled for the specified broadcast.",
    "errors": [
      {
        "message": "Live chat is not enabled for the specified broadcast.",
        "domain": "youtube.liveChat",
        "reason": "liveChatDisabled"
      }
    ]
  }
}]`

func TestAStreamingErrorWrappedInAnArrayIsStillRecognised(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}

	err := controller.ClassifyHTTP(http.StatusForbidden, []byte(streamingArrayBody),
		errors.New("streamList returned 403 Forbidden"))

	var unavailable *ChatUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error is %T, want ChatUnavailableError; the streaming endpoint "+
			"wraps its errors in an array and parsing only the object shape "+
			"made every streaming failure look temporary", err)
	}
}

// Being told to slow down for a moment is not the day's allowance running out.
// Conflating them switched YouTube off until midnight over a burst of
// requests -- and a retry loop is exactly what produces such a burst.
func TestARateLimitDoesNotWriteOffTheWholeDay(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := `{"error":{"code":403,"message":"Rate Limit Exceeded",` +
		`"errors":[{"reason":"rateLimitExceeded"}]}}`

	err := controller.ClassifyHTTP(http.StatusForbidden, []byte(body),
		errors.New("403"))

	if _, wrong := err.(*QuotaExhaustedError); wrong {
		t.Fatal("a momentary rate limit must not be reported as the daily quota")
	}
	if _, ok := err.(*RateLimitedError); !ok {
		t.Fatalf("error is %T, want RateLimitedError", err)
	}
	if controller.Quota.Exhausted() {
		t.Error("a rate limit must not mark the day's allowance spent")
	}
	if used := controller.Quota.Used(); used >= 10000 {
		t.Errorf("the ledger jumped to %d units over a rate limit", used)
	}
}

func TestTheDailyQuotaRunningOutIsStillRecordedProperly(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := `{"error":{"code":403,"message":"quota","errors":[{"reason":"dailyLimitExceeded"}]}}`

	if err := controller.ClassifyHTTP(http.StatusForbidden, []byte(body),
		errors.New("403")); err == nil {
		t.Fatal("expected an error")
	} else if _, ok := err.(*QuotaExhaustedError); !ok {
		t.Fatalf("error is %T, want QuotaExhaustedError", err)
	}
	if !controller.Quota.Exhausted() {
		t.Error("a genuinely spent allowance must still be recorded")
	}
}

// -- the gRPC shape -----------------------------------------------------------

// streamList is a server-streaming method, and its errors are documented as
// gRPC statuses -- PERMISSION_DENIED, FAILED_PRECONDITION and the rest --
// rather than as the Data API's own reason codes. A transcoded error can carry
// the status and no reasons at all, and reading only the reasons meant every
// one of these fell through as an unrecognised failure.

func grpcBody(code int, status, message string) []byte {
	return []byte(`[{"error":{"code":` + strconv.Itoa(code) +
		`,"message":"` + message + `","status":"` + status + `"}}]`)
}

// The worst of them: chat ending arrives as a 400, so the 403 catch-all never
// saw it and the loop retried an ended chat on a backoff, out loud, forever.
func TestAnEndedChatIsRecognisedFromItsGRPCStatus(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := grpcBody(400, "FAILED_PRECONDITION", "The live chat is no longer live.")

	err := controller.ClassifyHTTP(http.StatusBadRequest, body,
		errors.New("streamList returned 400 Bad Request"))

	var unavailable *ChatUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error is %T, want ChatUnavailableError", err)
	}
	if unavailable.Reason != "The live chat is no longer live." {
		t.Errorf("reason is %q; it is spoken aloud, so it has to be the sentence "+
			"rather than the status line", unavailable.Reason)
	}
}

func TestAMissingChatIsRecognisedFromItsGRPCStatus(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := grpcBody(404, "NOT_FOUND", "The live chat cannot be found.")

	err := controller.ClassifyHTTP(http.StatusNotFound, body, errors.New("404"))

	var unavailable *ChatUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error is %T, want ChatUnavailableError", err)
	}
}

// Documented as the rate limit, not the day's allowance. Getting this backwards
// switches YouTube off until midnight over a burst -- and unclassified, it did
// something just as bad: the loop moved straight on to the next transport and
// asked the same overloaded server faster.
func TestResourceExhaustedIsARateLimitNotTheDailyQuota(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := grpcBody(429, "RESOURCE_EXHAUSTED", "Rate limit exceeded.")

	err := controller.ClassifyHTTP(http.StatusTooManyRequests, body, errors.New("429"))

	if _, wrong := err.(*QuotaExhaustedError); wrong {
		t.Fatal("a momentary rate limit must not be reported as the daily quota")
	}
	if _, ok := err.(*RateLimitedError); !ok {
		t.Fatalf("error is %T, want RateLimitedError", err)
	}
	if controller.Quota.Exhausted() {
		t.Error("a rate limit must not mark the day's allowance spent")
	}
}

// On a members-only broadcast this is the likely shape of "you are signed in as
// the wrong channel". It is the end of the chain -- the poller asks the same
// question with the same sign-in -- so the sentence she hears is the last word
// on it and must not be a bare "unavailable".
func TestPermissionDeniedSaysSomethingSheCanActOn(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}

	err := controller.ClassifyHTTP(http.StatusForbidden,
		grpcBody(403, "PERMISSION_DENIED", ""), errors.New("403"))

	var unavailable *ChatUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error is %T, want ChatUnavailableError", err)
	}
	if unavailable.Reason == "" {
		t.Error("an empty reason is read aloud as silence")
	}
}

// The reason codes are more specific than the status and have to win: an
// exhausted quota also arrives as PERMISSION_DENIED, and reading the status
// first would report the day's allowance running out as a chat that is simply
// not there -- leaving the ledger unmarked and the next call going out anyway.
func TestAReasonCodeStillBeatsTheGRPCStatus(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	body := []byte(`[{"error":{"code":403,"message":"quota exceeded",` +
		`"status":"PERMISSION_DENIED","errors":[{"reason":"quotaExceeded"}]}}]`)

	err := controller.ClassifyHTTP(http.StatusForbidden, body, errors.New("403"))

	if _, ok := err.(*QuotaExhaustedError); !ok {
		t.Fatalf("error is %T, want QuotaExhaustedError", err)
	}
	if !controller.Quota.Exhausted() {
		t.Error("the ledger must record that the quota is gone")
	}
}

// Our own malformed request. Retrying will not fix it, but the poller is a
// different endpoint and may well work, so this stays an ordinary failure that
// falls through rather than one that stops the chain.
func TestAMalformedRequestFallsThroughToTheOtherTransport(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}
	original := errors.New("streamList returned 400")

	err := controller.ClassifyHTTP(http.StatusBadRequest,
		grpcBody(400, "INVALID_ARGUMENT", "Invalid value for liveChatId."), original)

	var unavailable *ChatUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatal("a bad request of ours must not be reported as the broadcast " +
			"having no chat: that stops the chain and waits for a new stream")
	}
	if err != original {
		t.Errorf("error is %v, want the original left alone", err)
	}
}
