// Several channels, one sign-in each.
//
// She runs a main channel and a music review channel, and they are separate
// YouTube channels with separate broadcasts, separate titles and separate chat
// -- so each needs its own consent and its own refresh token. One token file
// could only ever point at one of them, and switching would have meant signing
// out and signing in again, in a browser, mid-stream. That is not a thing this
// application can ask of her.
//
// So a token per channel, in data/youtube, named by the channel id YouTube
// itself assigns. What she calls each channel is a setting she can change; the
// id is not, which is why the id is what the file is named after and what the
// OBS profile binding in config.toml refers to.
//
// The quota is deliberately *not* split per channel. The daily allowance
// belongs to the Google Cloud project, not to a channel, so two channels draw
// on one budget of ten thousand units -- and a ledger per channel would happily
// spend twice the allowance and be surprised when Google stopped answering.
package youtube

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Account is one connected channel: who it is, and the consent to act for it.
type Account struct {
	ChannelID    string        `json:"channel_id"`
	ChannelTitle string        `json:"channel_title"`
	ConnectedAt  time.Time     `json:"connected_at"`
	Token        *oauth2.Token `json:"token"`
}

// Named is what to call this account out loud.
func (a Account) Named() string {
	if a.ChannelTitle != "" {
		return a.ChannelTitle
	}
	if a.ChannelID != "" {
		return a.ChannelID
	}
	return "YouTube"
}

// SaveAccount writes one channel's sign-in.
//
// An account with no channel id has not been identified yet -- the sign-in
// worked but YouTube has not been asked whose it is, which happens when the
// network is down at the wrong moment. It goes back to the single-token file it
// came from rather than being filed under a made-up id, and is migrated for
// real once the channel can be named.
func SaveAccount(account Account) error {
	if account.Token == nil {
		return &Error{Reason: "there is nothing to save for this account"}
	}
	if account.ChannelID == "" {
		return writeJSON(paths.TokenFile(), account.Token)
	}
	if _, err := paths.EnsureAccountsDir(); err != nil {
		return err
	}
	if account.ConnectedAt.IsZero() {
		account.ConnectedAt = time.Now()
	}
	return writeJSON(paths.AccountFile(account.ChannelID), account)
}

// LoadAccount reads one channel's sign-in.
func LoadAccount(channelID string) (Account, bool) {
	if channelID == "" {
		return legacyAccount()
	}
	data, err := os.ReadFile(paths.AccountFile(channelID))
	if err != nil {
		return Account{}, false
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil || account.Token == nil {
		slog.Warn("a stored YouTube sign-in is unusable", "channel", channelID, "error", err)
		return Account{}, false
	}
	// The id in the file wins over the one in the filename, but a file written
	// by hand may not carry one at all.
	if account.ChannelID == "" {
		account.ChannelID = channelID
	}
	return account, true
}

// legacyAccount reads the single youtube_token.json from before there were
// several channels. It has a token and nothing else; the channel it belongs to
// is discovered on the first call that names the channel.
func legacyAccount() (Account, bool) {
	data, err := os.ReadFile(paths.TokenFile())
	if err != nil {
		return Account{}, false
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		slog.Warn("the stored YouTube token is unusable", "error", err)
		return Account{}, false
	}
	return Account{Token: &token}, true
}

// Accounts lists every connected channel, in a stable order so the settings
// page and the spoken list agree with each other every time.
func Accounts() []Account {
	entries, err := os.ReadDir(paths.AccountsDir())
	if err != nil {
		return nil
	}

	found := make([]Account, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if account, ok := LoadAccount(id); ok {
			found = append(found, account)
		}
	}
	sort.Slice(found, func(left, right int) bool {
		if found[left].ChannelTitle != found[right].ChannelTitle {
			return found[left].ChannelTitle < found[right].ChannelTitle
		}
		return found[left].ChannelID < found[right].ChannelID
	})
	return found
}

// HasAccounts reports whether any channel has been connected.
//
// The old single token counts, so an installation that has never been through
// the new flow still reports itself as connected rather than silently asking
// her to sign in again.
func HasAccounts() bool {
	if len(Accounts()) > 0 {
		return true
	}
	_, err := os.Stat(paths.TokenFile())
	return err == nil
}

// ForgetAccount removes one channel's sign-in from disk.
func ForgetAccount(channelID string) {
	remove(paths.AccountFile(channelID))
	if channelID == "" {
		remove(paths.TokenFile())
	}
}

// ForgetAllAccounts removes every sign-in, including the pre-accounts one.
func ForgetAllAccounts() {
	for _, account := range Accounts() {
		remove(paths.AccountFile(account.ChannelID))
	}
	remove(paths.TokenFile())
}

func remove(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove a YouTube sign-in", "path", path, "error", err)
	}
}

// writeJSON saves a credential for this user only.
func writeJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := paths.EnsureDataDir(); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}
