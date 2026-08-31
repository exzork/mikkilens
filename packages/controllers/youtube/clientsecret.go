package youtube

import (
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// The OAuth client is read from her data directory, never built in.
//
// An earlier version embedded one, on the reasoning that RFC 8252 says an
// installed app cannot keep a secret anyway and Google issues desktop clients
// on that understanding -- which is true, and is why OBS ships its own. It is
// still the wrong thing to put in a public repository: a secret in a source
// tree is scraped within hours, the quota it carries is shared with whoever
// scraped it, and Google revokes the client rather than the copy. The credential
// then stops working for everyone at once, including her, mid-stream.
//
// So it lives beside her token and her keys, in data/client_secret.json, which
// is hers and is never committed. A build carries no credential at all, and the
// API key path stays as the route that needs no Cloud project.
//
// clientSource says where the OAuth client came from, for the settings page and
// for saying something useful when there is none.
type clientSource string

const (
	clientNone clientSource = "none"
	clientFile clientSource = "file"
)

// oauthConfig finds the OAuth client to use.
func oauthConfig() (*oauth2.Config, error) {
	settings, _, err := oauthConfigFrom(readClientSecretFile())
	return settings, err
}

func oauthConfigFrom(file []byte) (*oauth2.Config, clientSource, error) {
	if settings, ok := parseClientSecret(file); ok {
		return settings, clientFile, nil
	}
	return nil, clientNone, &NotAuthenticatedError{Reason: "there is no YouTube " +
		"sign-in set up: download OAuth desktop credentials from Google Cloud " +
		"and save them as data/client_secret.json, or use an API key instead"}
}

// parseClientSecret reports whether the bytes hold a usable OAuth client.
//
// A half-written file parses as JSON but carries no client id, so it has to be
// rejected on the id rather than on the parse -- otherwise a sign-in button
// would be offered that could only ever fail.
func parseClientSecret(data []byte) (*oauth2.Config, bool) {
	if len(data) == 0 {
		return nil, false
	}
	settings, err := google.ConfigFromJSON(data, Scope)
	if err != nil || settings.ClientID == "" {
		return nil, false
	}
	return settings, true
}

func readClientSecretFile() []byte {
	data, err := os.ReadFile(paths.ClientSecretFile())
	if err != nil {
		return nil
	}
	return data
}

// HasClientSecret reports whether signing in is possible at all.
func HasClientSecret() bool {
	_, _, err := oauthConfigFrom(readClientSecretFile())
	return err == nil
}

// ClientSource names where the OAuth client came from: "file" when
// data/client_secret.json holds one, "none" when signing in is not offered.
func ClientSource() string {
	_, source, err := oauthConfigFrom(readClientSecretFile())
	if err != nil {
		return string(clientNone)
	}
	return string(source)
}
