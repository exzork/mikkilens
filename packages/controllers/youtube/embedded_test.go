package youtube

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/core/paths"
	"github.com/exzork/mikkilens/packages/core/secret"
)

// sealed is a credential in the form a release build carries it.
func sealed(t *testing.T, body string) string {
	t.Helper()
	blob, err := secret.Seal([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// builtInto puts a sealed credential in this build for the length of one test,
// which is how a release build is simulated without linking one.
func builtInto(t *testing.T, blob string) {
	t.Helper()
	previous := embeddedClient
	embeddedClient = blob
	t.Cleanup(func() { embeddedClient = previous })
}

// writeClientSecret puts a credential where hers goes.
func writeClientSecret(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(paths.ClientSecretFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const otherClient = `{"installed":{"client_id":"456.apps.googleusercontent.com",` +
	`"client_secret":"hers","auth_uri":"https://accounts.google.com/o/oauth2/auth",` +
	`"token_uri":"https://oauth2.googleapis.com/token",` +
	`"redirect_uris":["http://localhost"]}}`

// The release build's whole purpose: she installs it and Connect works, with
// no Google Cloud project of her own and no file to place.
func TestAReleaseBuildCanSignInWithNoFileAtAll(t *testing.T) {
	isolate(t)
	builtInto(t, sealed(t, realClient))

	if !HasClientSecret() {
		t.Fatal("a build carrying a credential must offer signing in")
	}
	if got := ClientSource(); got != string(clientBuiltIn) {
		t.Errorf("ClientSource() is %q, want %q", got, clientBuiltIn)
	}

	settings, err := oauthConfig()
	if err != nil {
		t.Fatalf("oauthConfig: %v", err)
	}
	if settings.ClientID != "123.apps.googleusercontent.com" {
		t.Errorf("client id is %q", settings.ClientID)
	}
}

// Hers wins. Her own Cloud project carries her own quota rather than one
// shared with every other install, and if the shipped client is ever revoked
// this is what lets her keep streaming without waiting for a release.
func TestHerOwnFileBeatsTheBuiltInOne(t *testing.T) {
	isolate(t)
	builtInto(t, sealed(t, realClient))
	writeClientSecret(t, otherClient)

	settings, source, err := oauthConfigFrom(readClientSecretFile(), readEmbeddedClient())
	if err != nil {
		t.Fatalf("oauthConfigFrom: %v", err)
	}
	if source != clientFile {
		t.Errorf("source is %q, want %q", source, clientFile)
	}
	if settings.ClientID != "456.apps.googleusercontent.com" {
		t.Errorf("the built-in client was used over hers: %q", settings.ClientID)
	}
}

// A file she half-wrote, or emptied, must fall through to the built-in client
// rather than take the sign-in down with it.
func TestAnUnusableFileFallsBackToTheBuiltInOne(t *testing.T) {
	isolate(t)
	builtInto(t, sealed(t, realClient))

	for _, body := range []string{"", "{}", "not json"} {
		writeClientSecret(t, body)
		if got := ClientSource(); got != string(clientBuiltIn) {
			t.Errorf("with file %q, ClientSource() is %q, want %q", body, got, clientBuiltIn)
		}
	}
}

// A release built with a mangled blob must degrade to "no built-in client",
// not to a client that fails at Google's consent screen -- the one failure she
// cannot debug alone.
func TestABlobThatWillNotOpenIsNotACredential(t *testing.T) {
	isolate(t)

	for _, blob := range []string{"nonsense", sealed(t, realClient) + "AAAA", sealed(t, "{}")} {
		builtInto(t, blob)
		if source := ClientSource(); source == string(clientBuiltIn) {
			t.Errorf("blob %.12q... was accepted as a credential", blob)
		}
	}
}

// Sealing is worth nothing if the credential is also sitting in the source
// tree in the clear, which is the mistake this whole arrangement exists to
// avoid. A committed file here would be found by a scraper within hours.
func TestNoCredentialIsCommittedToTheRepository(t *testing.T) {
	root := repoRoot(t)

	// git rather than the filesystem: her own data/client_secret.json is
	// supposed to be sitting right there on her machine. The question is only
	// ever whether one is tracked, because that is the copy the world reads.
	if git, err := exec.LookPath("git"); err == nil {
		listed := exec.Command(git, "ls-files", "--",
			"data/client_secret.json",
			"*client_secret*.json",
			"*.pem", "*.p12")
		listed.Dir = root
		tracked, err := listed.Output()
		if err != nil {
			t.Skipf("could not ask git what is tracked: %v", err)
		}
		if found := strings.TrimSpace(string(tracked)); found != "" {
			t.Errorf("these credential files are committed:\n%s", found)
		}
	}

	// The Go sources are what a build links, and what anyone reads on GitHub.
	// Nothing in them may carry a real client id.
	sources, err := filepath.Glob(filepath.Join(root, "packages", "controllers", "youtube", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue // the tests use an obviously fake 123.apps.googleusercontent.com
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), ".apps.googleusercontent.com") {
			t.Errorf("%s contains what looks like a real client id", filepath.Base(path))
		}
	}
}

// repoRoot walks up to the module root, so the test finds the tree regardless
// of the directory `go test` happens to run it in.
func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the repository root")
		}
		directory = parent
	}
}
