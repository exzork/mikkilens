package youtube

import (
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Where the OAuth client comes from, and why there are two places.
//
// RFC 8252 is blunt that an installed app cannot keep a secret, and Google
// issues desktop OAuth clients on that understanding -- which is why OBS,
// rclone and gcloud all ship one. What must never happen is the credential
// sitting in a public source tree: that is scraped within hours, the quota it
// carries is shared with whoever scraped it, and Google revokes the client
// rather than the copy, so the sign-in dies for everyone at once, including
// her, mid-stream.
//
// So it is in neither the repository nor a plain string in the executable. It
// lives in two GitHub secrets, is sealed into the release build at link time
// by tools/packclient (see embedded.go), and never appears as readable text in
// anything published. That is obfuscation, not protection -- a determined
// reader gets it out of the binary -- and packages/core/secret says exactly
// what it does and does not buy. The mitigation for the day it does leak is
// that a replacement is one rotated secret and one release away, and that her
// own file below outlives the rotation.
//
// Her own file wins over the built-in one. Two reasons, both of them about her
// stream not stopping: a client from her own Cloud project carries her own
// quota rather than one shared with every other install, and if the shipped
// client is ever revoked she can drop a file in and keep going without waiting
// on a release.
//
// clientSource says which of the two it came from, for the settings page and
// for saying something useful when there is neither.
type clientSource string

const (
	clientNone    clientSource = "none"
	clientFile    clientSource = "file"
	clientBuiltIn clientSource = "built-in"
)

// oauthConfig finds the OAuth client to use.
func oauthConfig() (*oauth2.Config, error) {
	settings, _, err := oauthConfigFrom(readClientSecretFile(), readEmbeddedClient())
	return settings, err
}

func oauthConfigFrom(file, embedded []byte) (*oauth2.Config, clientSource, error) {
	if settings, ok := parseClientSecret(file); ok {
		return settings, clientFile, nil
	}
	if settings, ok := parseClientSecret(embedded); ok {
		return settings, clientBuiltIn, nil
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
	_, _, err := oauthConfigFrom(readClientSecretFile(), readEmbeddedClient())
	return err == nil
}

// ClientSource names where the OAuth client came from: "file" when
// data/client_secret.json holds one, "built-in" when the build carries one,
// "none" when signing in is not offered.
//
// It names the source and never the credential. Nothing in this package hands
// a client id or a secret to the HTTP API, the settings page or a log line.
func ClientSource() string {
	_, source, err := oauthConfigFrom(readClientSecretFile(), readEmbeddedClient())
	if err != nil {
		return string(clientNone)
	}
	return string(source)
}
