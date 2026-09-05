// Package secret hides a credential that is built into the binary.
//
// This is obfuscation, and it is worth being exact about what that buys.
// A credential shipped inside a desktop application is, in the end,
// extractable: the program has to be able to read it, so a person holding the
// program can read it too. Nothing here changes that. RFC 8252 says as much
// about installed apps, and Google issues desktop OAuth clients on that
// understanding.
//
// What sealing does buy is the difference between "found by a script" and
// "found by a person who set out to". The realistic threat to a shipped OAuth
// client is not reverse engineering; it is the automated scraping that finds a
// credential the moment it appears in a public source tree, a build log or a
// `strings` dump of a released executable, and burns the quota it carries
// until Google revokes the client -- for everyone at once, including her,
// mid-stream. Sealing removes it from all three of those places.
//
// So: the sealed form carries no readable client id and no readable secret,
// the key is derived rather than stored beside the ciphertext, and the nonce
// is fresh per build so two releases share no byte pattern to grep for. A
// determined reader still gets it out. Treat a shipped client as public
// eventually, and keep the mitigations that assume so -- see the
// packages/controllers/youtube notes.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// nonceSize is AES-GCM's standard nonce, which is also our key material.
const nonceSize = 12

// salt binds the derived key to this program, so a blob from somewhere else
// does not open here and vice versa. It is not a key and is not secret; the
// key is what comes out of the derivation below.
const salt = "mikkilens/sealed/v1"

// derive turns a nonce into the key that seals with it.
//
// Deriving rather than storing is the point: there is no key constant sitting
// next to the ciphertext to find, and because the nonce is fresh on every
// build, every build's blob is a different key over a different ciphertext.
func derive(nonce []byte) []byte {
	sum := sha256.Sum256(append([]byte(salt), nonce...))
	return sum[:]
}

// Seal packs plaintext into the single ASCII token that a build injects with
// -ldflags -X. The encoding is base64url without padding, so the result
// survives a command line on every shell we build on without quoting.
func Seal(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", errors.New("nothing to seal")
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	gcm, err := open(nonce)
	if err != nil {
		return "", err
	}

	sealed := gcm.Seal(nonce, nonce, plain, []byte(salt))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Unseal reverses Seal. It reports false rather than an error for anything
// wrong at all -- a truncated blob, a blob from another program, an empty
// build variable -- because every one of those means the same thing to the
// caller: this build carries no credential, carry on without one.
//
// The authentication tag is what makes that safe. Without it a corrupted blob
// would decrypt to plausible-looking rubbish and be handed onward as a
// credential, which fails later and somewhere unrelated.
func Unseal(blob string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(blob))
	if err != nil || len(raw) <= nonceSize {
		return nil, false
	}

	nonce := raw[:nonceSize]
	gcm, err := open(nonce)
	if err != nil {
		return nil, false
	}

	plain, err := gcm.Open(nil, nonce, raw[nonceSize:], []byte(salt))
	if err != nil {
		return nil, false
	}
	return plain, true
}

func open(nonce []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(derive(nonce))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
