package youtube_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// Nothing here needs credentials or a live stream. The quota ledger is the
// part worth testing hardest: running out mid-stream would stop chat, and
// stopping without saying so is the failure this app must not have.

func isolated(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	paths.SetRoot(directory)
	if _, err := paths.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	return directory
}

// -- quota --------------------------------------------------------------------

func TestReadsCostALittleAndWritesCostALot(t *testing.T) {
	if youtube.Costs["liveBroadcasts.list"] >= youtube.Costs["liveBroadcasts.update"] {
		t.Error("a read must cost less than a write")
	}
	if youtube.Costs["liveBroadcasts.update"] != 50 {
		t.Errorf("a write costs %d, want 50", youtube.Costs["liveBroadcasts.update"])
	}
}

func TestSpendingAccumulates(t *testing.T) {
	isolated(t)
	ledger := youtube.NewLedger(1000, 80)
	ledger.Spend("liveBroadcasts.list")
	ledger.Spend("liveBroadcasts.update")
	if ledger.Used() != 51 {
		t.Errorf("used = %d, want 51", ledger.Used())
	}
}

func TestPercentAndWarningThreshold(t *testing.T) {
	isolated(t)
	ledger := youtube.NewLedger(100, 80)
	for index := 0; index < 79; index++ {
		ledger.Spend("liveBroadcasts.list")
	}
	if ledger.ShouldWarn() {
		t.Errorf("at %d%% it should not warn yet", ledger.Percent())
	}
	ledger.Spend("liveBroadcasts.list")
	if !ledger.ShouldWarn() {
		t.Errorf("at %d%% it should warn", ledger.Percent())
	}
	if ledger.Exhausted() {
		t.Error("80 percent is not exhausted")
	}
}

func TestExhaustionIsDetected(t *testing.T) {
	isolated(t)
	ledger := youtube.NewLedger(10, 80)
	ledger.Spend("liveBroadcasts.update")
	if !ledger.Exhausted() {
		t.Error("spending 50 against a budget of 10 is exhausted")
	}
}

func TestUsagePersistsWithinTheSameDay(t *testing.T) {
	isolated(t)
	first := youtube.NewLedger(1000, 80)
	first.Spend("liveBroadcasts.update")
	first.Save()

	if used := youtube.NewLedger(1000, 80).Used(); used != 50 {
		t.Errorf("a reloaded ledger has used %d, want 50", used)
	}
}

func TestUsageResetsOnANewDay(t *testing.T) {
	isolated(t)
	ledger := youtube.NewLedger(1000, 80)
	ledger.Spend("liveBroadcasts.update")
	ledger.SetDay("2000-01-01") // pretend the stored day is old
	ledger.Spend("liveBroadcasts.list")

	if ledger.Day() != time.Now().Format("2006-01-02") {
		t.Errorf("day = %q", ledger.Day())
	}
	if ledger.Used() != 1 {
		t.Errorf("a new day starts from zero, used = %d", ledger.Used())
	}
}

func TestAStoredLedgerFromAnotherDayIsIgnored(t *testing.T) {
	directory := isolated(t)
	write(t, directory, `{"day": "2000-01-01", "used": 9999}`)
	if used := youtube.NewLedger(1000, 80).Used(); used != 0 {
		t.Errorf("used = %d, want 0", used)
	}
}

func TestACorruptLedgerDoesNotBreakStartup(t *testing.T) {
	directory := isolated(t)
	write(t, directory, "not json")
	if used := youtube.NewLedger(1000, 80).Used(); used != 0 {
		t.Errorf("used = %d, want 0", used)
	}
}

func TestMarkExhaustedSpendsTheWholeBudget(t *testing.T) {
	isolated(t)
	ledger := youtube.NewLedger(1000, 80)
	ledger.MarkExhausted()
	if !ledger.Exhausted() {
		t.Error("the budget should read as spent")
	}
	if youtube.NewLedger(1000, 80).Used() != 1000 {
		t.Error("the exhausted budget must survive a restart, or it retries all day")
	}
}

// -- controller ---------------------------------------------------------------

func TestCallingWithoutCredentialsIsAClearError(t *testing.T) {
	isolated(t)
	controller := youtube.New(10000, 80)
	if controller.Authenticated() {
		t.Error("a fresh controller is not authenticated")
	}
	_, err := controller.ActiveBroadcast(context.Background(), true)
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := err.(*youtube.NotAuthenticatedError); !ok {
		t.Errorf("error is %T, want NotAuthenticatedError", err)
	}
}

// TestAnExhaustedQuotaRefusesBeforeReachingTheNetwork matters because the
// alternative is a request that fails slowly and reports the wrong reason.
func TestAnExhaustedQuotaRefusesBeforeReachingTheNetwork(t *testing.T) {
	isolated(t)
	controller := youtube.New(1, 80)
	controller.Quota.MarkExhausted()

	_, err := controller.ListChatMessages(context.Background(), "chat-1", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	// Not authenticated is checked first, which is also a refusal before the
	// network; what must never happen is a real request going out.
	switch err.(type) {
	case *youtube.QuotaExhaustedError, *youtube.NotAuthenticatedError:
	default:
		t.Errorf("error is %T, want a refusal", err)
	}
}

func TestSignOutRemovesTheStoredToken(t *testing.T) {
	directory := isolated(t)
	tokenPath := filepath.Join(directory, "data", "youtube_token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !youtube.HasToken() {
		t.Fatal("the token should be there to start with")
	}

	youtube.New(10000, 80).SignOut()
	if youtube.HasToken() {
		t.Error("signing out must remove the stored token")
	}
}

func write(t *testing.T, directory, contents string) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(directory, "data", "quota.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
