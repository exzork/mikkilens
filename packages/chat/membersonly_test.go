package chat

import (
	"errors"
	"testing"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// Reading a members-only stream.
//
// The public chat page is fetched with no sign-in, which is what makes it
// free. It is also what makes it blind: a members-only broadcast serves an
// unauthenticated request a page with no chat in it, indistinguishable from a
// broadcast that has chat switched off. The page used to report that as chat
// being unavailable, the ingest loop believed it, stopped the chain, and put
// the broadcast on a two minute re-check that gave the same answer forever --
// so every members-only stream was silent, even though the Data API transports
// standing right behind the page ask as the channel that owns the broadcast
// and can read the chat perfectly well.
//
// These pin the rule that fixes it: only the last transport asked settles
// whether there is a chat.

func TestAPageThatSawNothingDoesNotSettleIt(t *testing.T) {
	err := error(&NotVisibleError{Reason: "no chat on the page"})

	if chatIsGone(err, true) {
		t.Error("the Data API transports can see a members-only chat, so they " +
			"have to be asked before the answer is no")
	}
	if !chatIsGone(err, false) {
		t.Error("with nothing left to ask, the answer stands -- otherwise a " +
			"broadcast with chat switched off is retried on a two second " +
			"backoff, out loud, forever")
	}
}

func TestTheDataAPIStillGetsTheLastWord(t *testing.T) {
	err := error(&youtube.ChatUnavailableError{Reason: "Live chat is not enabled"})

	// It asked as the owner and was told there is no chat. Falling through to
	// the poller only asks the same question a second way.
	if !chatIsGone(err, true) {
		t.Error("a signed-in transport saying there is no chat is the answer, " +
			"whatever is left in the chain")
	}
	if !chatIsGone(err, false) {
		t.Error("and it is still the answer at the end of the chain")
	}
}

func TestAnOrdinaryFailureIsStillJustAFailure(t *testing.T) {
	err := errors.New("connection reset")

	if chatIsGone(err, true) || chatIsGone(err, false) {
		t.Error("a network blip must be retried, not mistaken for a broadcast " +
			"with no chat")
	}
	if seesNoChat(err) {
		t.Error("nothing here says anything about whether a chat exists")
	}
}

// The rule is only worth anything if something that can see more is actually
// standing behind the page. Both Data API transports are signed in; the page
// is not.
func TestSomethingThatCanSeeMoreStandsBehindThePage(t *testing.T) {
	ingest := NewIngest(nil, IngestOptions{Transport: "auto"})

	candidates := ingest.Transports()
	if len(candidates) < 2 || candidates[0].Name() != "page" {
		t.Fatalf("transports = %v, want the page first with the Data API behind it",
			names(candidates))
	}
	for _, transport := range candidates[1:] {
		switch transport.(type) {
		case *streamTransport, *pollingTransport:
		default:
			t.Errorf("%s stands behind the page but is not a signed-in transport",
				transport.Name())
		}
	}
}

func names(transports []Transport) []string {
	found := make([]string, 0, len(transports))
	for _, transport := range transports {
		found = append(found, transport.Name())
	}
	return found
}
