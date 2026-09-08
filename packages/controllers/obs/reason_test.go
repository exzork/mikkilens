package obs

import (
	"errors"
	"testing"
)

// The wording is the library's and the number is the protocol's, so both are
// pinned here: this is the one place that decides whether she is told to go
// and look at a password or told to wait for a reconnection that is never
// coming.
func TestClassifyNamesWhatWentWrong(t *testing.T) {
	for _, testCase := range []struct{ name, text, want string }{
		// What OBS actually sends back, verbatim from a failed install.
		{"a refused password", "websocket: close 4009: Authentication failed.", ReasonAuth},
		{"the wording alone", "authentication failed", ReasonAuth},
		{"the code alone", "websocket: close 4009", ReasonAuth},

		{"OBS is not running", "dial tcp 127.0.0.1:4455: connect: connection refused", ReasonUnreachable},
		{"nothing listening on Windows", "dial tcp 127.0.0.1:4455: connectex: No connection could be made because the target machine actively refused it.", ReasonUnreachable},
		{"a host that does not resolve", "dial tcp: lookup obs-pc: no such host", ReasonUnreachable},
		{"no answer at all", "dial tcp 10.0.0.9:4455: i/o timeout", ReasonUnreachable},

		// Anything unrecognised keeps the raw text rather than being given a
		// confident wrong sentence.
		{"something else entirely", "unexpected EOF", ReasonUnknown},
		{"nothing", "", ReasonUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classify(errors.New(testCase.text)); got != testCase.want {
				t.Errorf("classify(%q) = %q, want %q", testCase.text, got, testCase.want)
			}
		})
	}

	if got := classify(nil); got != ReasonUnknown {
		t.Errorf("classify(nil) = %q, want empty", got)
	}
}

// The rule this encodes: a closed OBS is worth no announcement, because
// opening OBS fixes it. A rejected password is worth exactly one, because
// nothing she does next will fix it on its own.
func TestARejectedPasswordIsSaidOnceEvenBeforeAnythingConnected(t *testing.T) {
	var said []string
	controller := New(Options{
		Host: "localhost", Port: 4455,
		OnDisconnected: func(_, code string) { said = append(said, code) },
	})

	// Nothing has ever connected, which is the fresh-install case.
	controller.reportDisconnected("dial tcp: connection refused", ReasonUnreachable)
	if len(said) != 0 {
		t.Fatalf("a closed OBS announced %v, want silence", said)
	}

	controller.reportDisconnected("websocket: close 4009", ReasonAuth)
	controller.reportDisconnected("websocket: close 4009", ReasonAuth)
	controller.reportDisconnected("websocket: close 4009", ReasonAuth)
	if len(said) != 1 || said[0] != ReasonAuth {
		t.Errorf("the retry loop said %v, want one %q", said, ReasonAuth)
	}

	if got := controller.LastReason(); got != ReasonAuth {
		t.Errorf("LastReason is %q, want %q", got, ReasonAuth)
	}
}
