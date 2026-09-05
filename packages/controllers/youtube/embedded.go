package youtube

import (
	"encoding/json"
	"log/slog"

	"github.com/exzork/mikkilens/packages/core/secret"
)

// embeddedClient is the OAuth client the release build carries, sealed.
//
// Nothing sets it here. It is empty in every ordinary build -- `go build`,
// `make engine`, `go test`, air in the dev loop -- and stays empty in a clone
// of this repository, because there is no credential in this repository to
// find. The release workflow is the only thing that fills it, with
//
//	-X github.com/exzork/mikkilens/packages/controllers/youtube.embeddedClient=<blob>
//
// where the blob is built by tools/packclient out of the two GitHub secrets,
// GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET, and sealed by
// packages/core/secret so that neither the id nor the secret appears as
// readable text in the shipped executable.
//
// It is a string rather than a []byte because -X can only set a string, and
// base64url rather than raw bytes because it has to survive a command line.
var embeddedClient string

// readEmbeddedClient is the built-in credential as a client_secret.json body,
// or nil when this build carries none.
//
// Every failure is nil rather than an error. A build with no credential, a
// blob truncated by a shell, a blob from an older sealing -- all of them mean
// the same thing to the caller: there is no built-in client here, fall back to
// hers. The one that deserves saying out loud is a blob that is present and
// will not open, because that is a release built wrong rather than a release
// built without.
func readEmbeddedClient() []byte {
	if embeddedClient == "" {
		return nil
	}

	data, ok := secret.Unseal(embeddedClient)
	if !ok {
		// No value in the log line: whatever is in there did not open, and
		// printing it would be the leak this whole file exists to avoid.
		slog.Warn("this build carries a YouTube sign-in credential that will not open; " +
			"signing in will need data/client_secret.json until the build is fixed")
		return nil
	}
	if !json.Valid(data) {
		slog.Warn("the built-in YouTube sign-in credential is not valid JSON")
		return nil
	}
	return data
}
