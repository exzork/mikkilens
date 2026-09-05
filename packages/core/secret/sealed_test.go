package secret

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// shellSensitive is every character a shell -- PowerShell or sh -- would
// quote, expand or glob rather than pass through untouched. base64url avoids
// all of them, which is why the blob travels as one bare -ldflags argument.
const shellSensitive = "+/= '" + `"$` + "`" + `\*?&|<>;()[]{}!#`

const credential = `{"installed":{"client_id":"123.apps.googleusercontent.com","client_secret":"shh"}}`

func TestWhatIsSealedComesBack(t *testing.T) {
	blob, err := Seal([]byte(credential))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	got, ok := Unseal(blob)
	if !ok {
		t.Fatal("a blob this program sealed did not open")
	}
	if !bytes.Equal(got, []byte(credential)) {
		t.Errorf("got %q, want %q", got, credential)
	}
}

// The whole point. If the client id or the secret survives into the blob as
// readable text, `strings` on the released executable hands it to the first
// scraper that looks, and sealing has bought nothing.
func TestTheSealedFormReadsAsNothing(t *testing.T) {
	blob, err := Seal([]byte(credential))
	if err != nil {
		t.Fatal(err)
	}

	for _, giveaway := range []string{"client_id", "client_secret", "shh", "googleusercontent", "installed"} {
		if strings.Contains(blob, giveaway) {
			t.Errorf("the sealed blob still contains %q", giveaway)
		}
	}
}

// Two builds of the same credential must not share a byte pattern, or the
// blob becomes a stable fingerprint: find it once by other means and you can
// then recognise it in every release without doing the work again.
func TestTwoBuildsOfTheSameCredentialLookUnrelated(t *testing.T) {
	first, err := Seal([]byte(credential))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal([]byte(credential))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("sealing the same credential twice produced the same blob")
	}
}

// The build variable is empty in every build that was given no credential,
// which is the common case and must not be treated as a broken one.
func TestNothingSealedIsNotACredential(t *testing.T) {
	for _, blob := range []string{"", "   ", "not base64 !!", "aaaa", "AAAAAAAAAAAAAAAAAAAA"} {
		if _, ok := Unseal(blob); ok {
			t.Errorf("%q opened as a credential", blob)
		}
	}
	if _, err := Seal(nil); err == nil {
		t.Error("sealing nothing must be refused")
	}
}

// A blob damaged in transit -- truncated by a shell, mangled by a copy and
// paste -- must be refused rather than opened into plausible rubbish that
// fails later as a confusing OAuth error.
func TestATamperedBlobIsRefused(t *testing.T) {
	blob, err := Seal([]byte(credential))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}
	// A ciphertext byte, past the nonce, so this is the tag catching it rather
	// than the key simply having changed.
	raw[len(raw)/2] ^= 0x01
	if _, ok := Unseal(base64.RawURLEncoding.EncodeToString(raw)); ok {
		t.Error("a blob with a changed byte still opened")
	}
	if _, ok := Unseal(blob[:len(blob)-4]); ok {
		t.Error("a truncated blob still opened")
	}
}

// It travels as one -ldflags -X argument through PowerShell and sh, so it has
// to stay clear of quoting, globbing and padding characters.
func TestTheBlobSurvivesACommandLine(t *testing.T) {
	for i := 0; i < 50; i++ {
		blob, err := Seal([]byte(credential))
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(blob, shellSensitive) {
			t.Fatalf("blob %q contains a character a shell would touch", blob)
		}
	}
}
