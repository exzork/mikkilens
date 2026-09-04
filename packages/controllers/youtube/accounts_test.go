package youtube

import (
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/exzork/mikkilens/packages/core/paths"
)

func TestAccountsRoundTrip(t *testing.T) {
	isolate(t)

	main := Account{
		ChannelID:    "UCmain",
		ChannelTitle: "Mikki",
		Token:        &oauth2.Token{RefreshToken: "one"},
	}
	music := Account{
		ChannelID:    "UCmusic",
		ChannelTitle: "Mikki Music",
		Token:        &oauth2.Token{RefreshToken: "two"},
	}
	for _, account := range []Account{main, music} {
		if err := SaveAccount(account); err != nil {
			t.Fatalf("saving %s: %v", account.ChannelID, err)
		}
	}

	// The point of the whole change: two sign-ins on disk at once, neither
	// having overwritten the other.
	if all := Accounts(); len(all) != 2 {
		t.Fatalf("expected 2 accounts, got %d: %+v", len(all), all)
	}

	loaded, ok := LoadAccount("UCmusic")
	if !ok {
		t.Fatal("the music account did not load back")
	}
	if loaded.Token.RefreshToken != "two" || loaded.ChannelTitle != "Mikki Music" {
		t.Fatalf("loaded the wrong account: %+v", loaded)
	}

	// Signing out of one must leave the other alone. Signing her out of both
	// because one token was refused is the failure this guards.
	ForgetAccount("UCmusic")
	if _, ok := LoadAccount("UCmusic"); ok {
		t.Error("the music account survived being forgotten")
	}
	if _, ok := LoadAccount("UCmain"); !ok {
		t.Error("forgetting one account took the other with it")
	}
}

// A sign-in is a credential, so it is written for this user only.
//
// Skipped on Windows, which does not map the mode bits at all -- every file Go
// writes there comes back as -rw-rw-rw- regardless of what was asked for, so
// asserting on them would be testing the operating system rather than this
// code. The 0o600 is still worth passing: it is what protects the file
// everywhere else, and it costs nothing where it is ignored.
func TestAccountFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not carry POSIX file modes")
	}
	isolate(t)

	account := Account{ChannelID: "UCmain", Token: &oauth2.Token{RefreshToken: "one"}}
	if err := SaveAccount(account); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(paths.AccountFile("UCmain"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("account file is group- or world-readable: %v", mode)
	}
}

// An account that has not been identified yet goes back to the single token
// file it came from, rather than being filed under an invented id.
func TestUnidentifiedAccountStaysInTheLegacyFile(t *testing.T) {
	isolate(t)

	if err := SaveAccount(Account{Token: &oauth2.Token{RefreshToken: "one"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.TokenFile()); err != nil {
		t.Fatalf("expected the pre-accounts token file: %v", err)
	}
	if all := Accounts(); len(all) != 0 {
		t.Fatalf("an unidentified sign-in became an account: %+v", all)
	}

	// It still counts as connected, so an installation that has never seen the
	// new flow is not told to sign in again.
	if !HasAccounts() {
		t.Error("the pre-accounts token did not count as connected")
	}

	loaded, ok := LoadAccount("")
	if !ok || loaded.Token.RefreshToken != "one" {
		t.Fatalf("the pre-accounts token did not load back: %+v", loaded)
	}
}

func TestAccountFileNameRejectsPathSeparators(t *testing.T) {
	isolate(t)

	// A malformed id must become a useless filename inside the accounts
	// directory, never a write somewhere else.
	nasty := paths.AccountFile("../../escaped")
	if want := paths.AccountsDir(); !hasPrefix(nasty, want) {
		t.Errorf("account file escaped the data directory: %s", nasty)
	}
}

func hasPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

func TestSaveAccountStampsConnectedAt(t *testing.T) {
	isolate(t)

	before := time.Now().Add(-time.Second)
	if err := SaveAccount(Account{
		ChannelID: "UCmain", Token: &oauth2.Token{RefreshToken: "one"},
	}); err != nil {
		t.Fatal(err)
	}

	loaded, ok := LoadAccount("UCmain")
	if !ok {
		t.Fatal("the account did not load back")
	}
	if loaded.ConnectedAt.Before(before) {
		t.Errorf("connected_at was not stamped: %v", loaded.ConnectedAt)
	}
}

func TestNamedFallsBackToSomethingTrue(t *testing.T) {
	cases := []struct {
		account Account
		want    string
	}{
		{Account{ChannelTitle: "Mikki Music", ChannelID: "UCmusic"}, "Mikki Music"},
		{Account{ChannelID: "UCmusic"}, "UCmusic"},
		{Account{}, "YouTube"},
	}
	for _, one := range cases {
		if got := one.account.Named(); got != one.want {
			t.Errorf("Named() = %q, want %q", got, one.want)
		}
	}
}
