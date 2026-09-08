package chat

import (
	"encoding/json"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// These two payloads are trimmed from what YouTube actually sent for a real
// batch of ten gifted memberships, which is the only reason they are the right
// shape: the purchase keeps everything but its id inside a header, and the
// redemption names its giver in prose and gives no id to match them by.

const giftPurchaseJSON = `{
  "liveChatSponsorshipsGiftPurchaseAnnouncementRenderer": {
    "id": "purchase-1",
    "timestampUsec": "1788832876135932",
    "authorExternalChannelId": "UCgifter",
    "header": {
      "liveChatSponsorshipsHeaderRenderer": {
        "authorName": {"simpleText": "@exzork_"},
        "authorBadges": [
          {"liveChatAuthorBadgeRenderer": {"tooltip": "Member (6 months)",
            "customThumbnail": {"thumbnails": []}}}
        ],
        "primaryText": {"runs": [
          {"text": "Sent "}, {"text": "10"}, {"text": " "},
          {"text": "Mikkiru"}, {"text": " gift memberships"}
        ]}
      }
    }
  }
}`

const giftRedemptionJSON = `{
  "liveChatSponsorshipsGiftRedemptionAnnouncementRenderer": {
    "id": "redeem-1",
    "timestampUsec": "1788832876897806",
    "authorExternalChannelId": "UCrecipient",
    "authorName": {"simpleText": "@FioChan324"},
    "message": {"runs": [
      {"text": "received a gift membership by "},
      {"text": "@exzork_"}
    ]}
  }
}`

func parseItem(t *testing.T, raw string) Message {
	t.Helper()
	var item chatItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("could not read the payload: %v", err)
	}
	message, ok := item.message()
	if !ok {
		t.Fatal("the payload produced no message at all")
	}
	return message
}

// The purchase announcement keeps its author one level down, which is how it
// came out with no name on it: read from the top level there is nobody there.
func TestGiftPurchaseNamesWhoSentItAndHowMany(t *testing.T) {
	message := parseItem(t, giftPurchaseJSON)

	if message.Author != "@exzork_" {
		t.Errorf("author is %q, want the name from the header", message.Author)
	}
	if message.AuthorChannelID != "UCgifter" {
		t.Errorf("channel id is %q, want UCgifter", message.AuthorChannelID)
	}
	if !message.IsGift {
		t.Error("a gift purchase must be a gift")
	}
	if message.IsMember {
		t.Error("sending gifts is not the same as joining")
	}
	if message.GiftCount != 10 {
		t.Errorf("count is %d, want 10 -- the number is the whole point",
			message.GiftCount)
	}
	if !message.AuthorIsMember {
		t.Error("the header's badges belong to the author too")
	}
}

// The recipients are the half that was dropped entirely: the renderer was not
// in the union, so ten people got a membership and none of them was named.
func TestGiftRedemptionNamesTheRecipientAndTheGiver(t *testing.T) {
	message := parseItem(t, giftRedemptionJSON)

	if message.Author != "@FioChan324" {
		t.Errorf("author is %q, want the recipient", message.Author)
	}
	if !message.IsGiftReceived {
		t.Error("a redemption must be a received gift")
	}
	if message.GifterName != "@exzork_" {
		t.Errorf("gifter is %q, want the name out of the runs", message.GifterName)
	}
	if message.Text != "" {
		t.Errorf("text is %q, want the boilerplate dropped", message.Text)
	}
}

// End to end: what the voice says about a batch, which is the thing that was
// wrong out loud.
func TestABatchOfGiftsIsReadAsPeopleRatherThanSilence(t *testing.T) {
	ingest := &Ingest{seen: map[string]bool{}}
	reader := NewReader(nil, nil, i18n.Load("id"), config.Default().Chat, nil)

	purchase := parseItem(t, giftPurchaseJSON)
	ingest.noteGiftLocked(&purchase)
	if spoken := reader.Render(purchase); spoken != "@exzork_ menghadiahkan 10 membership." {
		t.Errorf("the purchase reads as %q", spoken)
	}

	redemption := parseItem(t, giftRedemptionJSON)
	ingest.noteGiftLocked(&redemption)
	if redemption.GiftIndex != 1 || redemption.GiftTotal != 10 {
		t.Errorf("the batch joined up as %d of %d, want 1 of 10",
			redemption.GiftIndex, redemption.GiftTotal)
	}
	want := "@FioChan324 mendapat hadiah membership dari @exzork_."
	if spoken := reader.Render(redemption); spoken != want {
		t.Errorf("the redemption reads as %q, want %q", spoken, want)
	}
}
