package youtube

import (
	"errors"
	"testing"

	"golang.org/x/oauth2"
)

// A Google Cloud project still in Testing expires refresh tokens after seven
// days. For such a build this is not an edge case, it is next week -- and the
// failure has to be told to her, because she cannot see that YouTube quietly
// switched itself off.

func TestARevokedGrantIsRecognised(t *testing.T) {
	for name, err := range map[string]error{
		"typed": &oauth2.RetrieveError{ErrorCode: "invalid_grant"},
		"wrapped": errors.Join(errors.New("refreshing token"),
			&oauth2.RetrieveError{ErrorCode: "invalid_grant"}),
		"string only": errors.New(`oauth2: cannot fetch token: 400 Bad Request` +
			` Response: {"error":"invalid_grant","error_description":"Token has been expired or revoked."}`),
	} {
		if !isRevokedGrant(err) {
			t.Errorf("%s: an expired refresh token must be recognised", name)
		}
	}
}

// Getting this wrong in the other direction is worse than the failure itself:
// a network blip must not throw away a perfectly good refresh token and make
// her redo the consent screen for nothing.
func TestATemporaryFailureIsNotMistakenForARevokedGrant(t *testing.T) {
	for name, err := range map[string]error{
		"nothing":     nil,
		"network":     errors.New("dial tcp: lookup oauth2.googleapis.com: no such host"),
		"server side": &oauth2.RetrieveError{ErrorCode: "server_error"},
		"rate limit":  errors.New("oauth2: cannot fetch token: 429 Too Many Requests"),
	} {
		if isRevokedGrant(err) {
			t.Errorf("%s: this must not be treated as a dead credential", name)
		}
	}
}

func TestAnExpiredSignInIsItsOwnKindOfProblem(t *testing.T) {
	var err error = &ExpiredCredentialsError{Reason: "expired"}

	// The engine branches on this to say "connect again" rather than the
	// "never connected" message, so the two must not be interchangeable.
	if _, wrong := err.(*NotAuthenticatedError); wrong {
		t.Error("an expired sign-in must not be a NotAuthenticatedError")
	}
	var expired *ExpiredCredentialsError
	if !errors.As(err, &expired) {
		t.Error("errors.As must find the expired-credentials error")
	}
}
