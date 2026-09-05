// Command packclient seals the OAuth client into the token a release build
// links in.
//
// It reads GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET from the
// environment -- in CI those are the two GitHub secrets -- and writes one
// base64url token to stdout. Everything it has to say to a person goes to
// stderr, so a caller can take stdout whole:
//
//	go run ./tools/packclient
//
// Nothing here prints the id or the secret, and neither does anything it feeds.
// A build log is public on a public repository, and a credential that reaches
// one is burned regardless of what the workflow file intended.
//
// It exits 0 having printed nothing when neither variable is set. That is the
// ordinary case for a fork, a pull request and anyone's own clone: no
// credential to seal is not an error, it is a build that ships without a
// built-in sign-in, which is exactly what the code below expects.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/exzork/mikkilens/packages/core/secret"
)

// installed is the shape Google hands out for a desktop client, and the shape
// google.ConfigFromJSON reads back. Rebuilding it from the two halves rather
// than carrying the whole downloaded file keeps the secret store to two plain
// values, which is what a GitHub secret is good at holding.
type installed struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURI      string   `json:"auth_uri"`
	TokenURI     string   `json:"token_uri"`
	RedirectURIs []string `json:"redirect_uris"`
}

func main() {
	id := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"))

	if id == "" && clientSecret == "" {
		fmt.Fprintln(os.Stderr, "packclient: no OAuth client in the environment; "+
			"this build will ship without a built-in YouTube sign-in")
		return
	}

	// One of the two set is a secret that was renamed, misspelled or never
	// added to the repository. Shipping half a credential produces a sign-in
	// that fails at Google's consent screen, which is the one failure she
	// cannot debug alone -- so fail the build here instead.
	if id == "" || clientSecret == "" {
		die("GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET must be set together")
	}
	if !strings.HasSuffix(id, ".apps.googleusercontent.com") {
		die("GOOGLE_OAUTH_CLIENT_ID does not look like a Google client id " +
			"(it should end in .apps.googleusercontent.com)")
	}

	body, err := json.Marshal(map[string]installed{"installed": {
		ClientID:     id,
		ClientSecret: clientSecret,
		AuthURI:      "https://accounts.google.com/o/oauth2/auth",
		TokenURI:     "https://oauth2.googleapis.com/token",
		// The consent flow replaces this with the loopback port it actually
		// listened on, but a desktop client is not usable without one present.
		RedirectURIs: []string{"http://localhost"},
	}})
	if err != nil {
		die(err.Error())
	}

	blob, err := secret.Seal(body)
	if err != nil {
		die(err.Error())
	}

	fmt.Fprintf(os.Stderr, "packclient: sealed an OAuth client, %d characters\n", len(blob))
	fmt.Print(blob)
}

func die(reason string) {
	fmt.Fprintln(os.Stderr, "packclient: "+reason)
	os.Exit(1)
}
