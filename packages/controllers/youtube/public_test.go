package youtube_test

import (
	"context"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
)

// The API key is the way in that most people will actually get working, so
// what is tested here is the part they touch: pasting a link, and being told
// clearly which half of YouTube they have.

func TestAVideoLinkIsAcceptedInEveryShapeSheMightPasteIt(t *testing.T) {
	const wanted = "dQw4w9WgXcQ"

	for _, pasted := range []string{
		wanted,
		"https://www.youtube.com/watch?v=" + wanted,
		"https://www.youtube.com/watch?v=" + wanted + "&t=42s",
		"https://youtu.be/" + wanted,
		"https://www.youtube.com/live/" + wanted,
		"  https://www.youtube.com/watch?v=" + wanted + "  ",
	} {
		if got := youtube.ParseVideoID(pasted); got != wanted {
			t.Errorf("ParseVideoID(%q) = %q, want %q", pasted, got, wanted)
		}
	}
}

func TestSomethingThatIsNotAVideoLinkIsRejectedRatherThanGuessedAt(t *testing.T) {
	for _, pasted := range []string{"", "   ", "not a link", "https://www.youtube.com/"} {
		if got := youtube.ParseVideoID(pasted); got != "" {
			t.Errorf("ParseVideoID(%q) = %q, want empty", pasted, got)
		}
	}
}

func TestAChannelLinkIsAcceptedInEveryShapeSheMightPasteIt(t *testing.T) {
	const wanted = "UCuAXFkgsw1L7xaCfnd5JJOw"

	for _, pasted := range []string{
		wanted,
		"https://www.youtube.com/channel/" + wanted,
		"https://www.youtube.com/channel/" + wanted + "/live",
	} {
		if got := youtube.ParseChannelID(pasted); got != wanted {
			t.Errorf("ParseChannelID(%q) = %q, want %q", pasted, got, wanted)
		}
	}
}

// A handle cannot be resolved without a lookup of its own, so it must come
// back empty and be reported, not silently treated as a channel id.
func TestAHandleIsNotMistakenForAChannelID(t *testing.T) {
	if got := youtube.ParseChannelID("https://www.youtube.com/@mikki"); got != "" {
		t.Errorf("ParseChannelID(handle) = %q, want empty", got)
	}
}

func TestWithNoKeyAndNoSignInThereIsNoAccessAtAll(t *testing.T) {
	isolated(t)
	controller := youtube.New(config.YouTube{QuotaBudget: 10000, QuotaWarnPercent: 80}, "")

	if err := controller.StartPublic(context.Background()); err != nil {
		t.Fatalf("having no key is not an error: %v", err)
	}
	if access := controller.Access(); access != youtube.AccessNone {
		t.Errorf("access is %q, want %q", access, youtube.AccessNone)
	}
	if controller.Available() {
		t.Error("nothing is configured, so nothing is available")
	}
}

func TestAKeyAloneGivesTheReadableHalfOfYouTube(t *testing.T) {
	isolated(t)
	controller := youtube.New(
		config.YouTube{QuotaBudget: 10000, QuotaWarnPercent: 80},
		"AIza-not-a-real-key",
	)

	if err := controller.StartPublic(context.Background()); err != nil {
		t.Fatalf("StartPublic: %v", err)
	}
	if access := controller.Access(); access != youtube.AccessPublic {
		t.Errorf("access is %q, want %q", access, youtube.AccessPublic)
	}
	if !controller.Available() {
		t.Error("a key is enough to read, so YouTube is available")
	}
	if controller.Authenticated() {
		t.Error("a key is not a sign-in")
	}
}

// The refusal must arrive before any request goes out, and must say what to do
// about it -- "403" spoken aloud helps nobody.
func TestChangingTheTitleWithOnlyAKeySaysToSignIn(t *testing.T) {
	isolated(t)
	controller := youtube.New(
		config.YouTube{QuotaBudget: 10000, QuotaWarnPercent: 80},
		"AIza-not-a-real-key",
	)
	if err := controller.StartPublic(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := controller.SetTitle(context.Background(), "Main Minecraft")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if _, ok := err.(*youtube.NotAuthenticatedError); !ok {
		t.Fatalf("error is %T, want NotAuthenticatedError", err)
	}
	if controller.Quota.Used() != 0 {
		t.Error("a refusal must not spend quota on a request it never sent")
	}
}

// Finding which video is live is a hundred times the cost of reading it, which
// is the whole reason the answer is cached. If that ever stops being true the
// caching is pointless and the comment explaining it is wrong.
func TestFindingTheLiveVideoIsTheExpensiveCall(t *testing.T) {
	if youtube.Costs["search.list"] <= youtube.Costs["videos.list"] {
		t.Error("searching must cost more than reading, or the cache is pointless")
	}
	if youtube.Costs["search.list"] != 100 {
		t.Errorf("search.list costs %d, want 100", youtube.Costs["search.list"])
	}
}

// With a key but nothing to watch, the message has to name the missing thing.
// "no active broadcast" would send her looking at OBS for a problem that is
// really an empty settings field.
func TestAKeyWithNoChannelSaysWhatIsMissing(t *testing.T) {
	isolated(t)
	controller := youtube.New(
		config.YouTube{QuotaBudget: 10000, QuotaWarnPercent: 80},
		"AIza-not-a-real-key",
	)
	if err := controller.StartPublic(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := controller.ActiveBroadcast(context.Background(), true)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "channel") {
		t.Errorf("error is %q, which does not say the channel is missing", err)
	}
	if controller.Quota.Used() != 0 {
		t.Error("a refusal must not spend quota on a request it never sent")
	}
}
