package youtube

import (
	"net/http"
	"strings"
	"testing"
)

// These test unexported behaviour, so they live in the package rather than
// beside the black-box tests in quota_test.go.

const realClient = `{"installed":{"client_id":"123.apps.googleusercontent.com",` +
	`"client_secret":"shh","auth_uri":"https://accounts.google.com/o/oauth2/auth",` +
	`"token_uri":"https://oauth2.googleapis.com/token",` +
	`"redirect_uris":["http://localhost"]}}`

// A usable file has to be recognised as one, or the sign-in button is offered
// and can only ever fail -- and failing at the consent screen is the one
// failure she cannot debug alone.
func TestAUsableFileIsRecognised(t *testing.T) {
	settings, ok := parseClientSecret([]byte(realClient))
	if !ok {
		t.Fatal("a valid client_secret.json was not recognised")
	}
	if !strings.HasSuffix(settings.ClientID, ".apps.googleusercontent.com") {
		t.Errorf("client id %q does not look like a Google client id", settings.ClientID)
	}
	if settings.RedirectURL == "" {
		t.Error("a desktop client needs a redirect URL, or consent cannot complete")
	}
}

// A build from this source tree carries no credential.
//
// Only the release workflow links one in, out of two GitHub secrets that are
// not in the repository -- so a clone, the dev loop and this test suite all
// find nothing built in, and must say "none" rather than offer a sign-in they
// cannot complete. That the release build does carry one is embedded_test.go's
// business; this is the invariant that keeps the credential out of the tree.
func TestASourceBuildCarriesNoCredential(t *testing.T) {
	isolate(t)

	if embeddedClient != "" {
		t.Fatal("a credential is linked into this build; it must only ever come " +
			"from the release workflow's -ldflags, never from the source tree")
	}
	if got := ClientSource(); got != string(clientNone) {
		t.Errorf("ClientSource() is %q with no file present, want %q", got, clientNone)
	}
	if HasClientSecret() {
		t.Error("signing in must not be offered when there is no client_secret.json")
	}
}

func TestNothingAtAllIsNotAClient(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("not json"), []byte("{}")} {
		if _, ok := parseClientSecret(data); ok {
			t.Errorf("%q must not parse as a usable OAuth client", data)
		}
	}
}

func TestTheDownloadedFileIsUsed(t *testing.T) {
	settings, source, err := oauthConfigFrom([]byte(realClient), nil)
	if err != nil {
		t.Fatalf("oauthConfigFrom: %v", err)
	}
	if source != clientFile {
		t.Errorf("source is %q, want %q", source, clientFile)
	}
	if settings.ClientID != "123.apps.googleusercontent.com" {
		t.Errorf("client id is %q", settings.ClientID)
	}
}

// A half-written file must be refused rather than accepted as a client, or the
// sign-in button appears and dies at the consent screen.
func TestAnUnusableFileIsNotAClient(t *testing.T) {
	_, source, err := oauthConfigFrom([]byte("{}"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if source != clientNone {
		t.Errorf("source is %q, want %q", source, clientNone)
	}
}

func TestWithNoClientAtAllTheErrorSaysWhatToDo(t *testing.T) {
	_, source, err := oauthConfigFrom(nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if source != clientNone {
		t.Errorf("source is %q, want %q", source, clientNone)
	}
	if _, ok := err.(*NotAuthenticatedError); !ok {
		t.Errorf("error is %T, want NotAuthenticatedError", err)
	}
	if !strings.Contains(err.Error(), "client_secret.json") {
		t.Errorf("the error must say where to put the file, got %q", err)
	}
}

// -- streaming authorization --------------------------------------------------

func TestStreamingWithNoWayInIsRefusedRatherThanSentUnauthorized(t *testing.T) {
	isolate(t)

	controller := &Controller{Quota: NewLedger(10000, 80)}

	request, err := http.NewRequest(http.MethodGet, "https://example.invalid/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = controller.AuthorizeStream(request)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if _, ok := err.(*NotAuthenticatedError); !ok {
		t.Errorf("error is %T, want NotAuthenticatedError", err)
	}
}
