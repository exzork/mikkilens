package speakable

import "testing"

// Lines taken from a real Indonesian stream's chat, as MikkiLens received them.
func TestIndonesianChatIsWrittenOut(t *testing.T) {
	for _, test := range []example{
		{"wkkwkwkwk", "wkwk"},
		{"lucu banget oawkoawkoawk", "lucu banget wkwk"},
		{"marah loh yah wkwkkw", "marah loh yah wkwk"},
		{"wkwk wkwkwk wkwk", "wkwk"},
		{"aku jga degdegan", "aku juga degdegan"},
		{"yg ini bgt, udh lah", "yang ini banget, udah lah"},
		{"Gak mau", "Nggak mau"},
		{"kakkkk mantappp", "kakk mantapp"},
		{"hahahahaha", "hahaha"},
		// Ordinary words made of the same letters are not laughter.
		{"kaka awak kok", "kaka awak kok"},
		{"reii aman , heart rate nya naik", "reii aman , heart rate nya naik"},
	} {
		if got := Chat(test.written, "id"); got != test.spoken {
			t.Errorf("Chat(%q)\n got %q\nwant %q", test.written, got, test.spoken)
		}
	}
}

func TestEnglishChatIsWrittenOut(t *testing.T) {
	for _, test := range []example{
		{"idk tbh lol", "I don't know to be honest lol"},
		{"wkwkwk", "haha"},
		{"thx u", "thanks you"},
	} {
		if got := Chat(test.written, "en"); got != test.spoken {
			t.Errorf("Chat(%q)\n got %q\nwant %q", test.written, got, test.spoken)
		}
	}
}

// Handles as they arrive from YouTube, and the name a voice can say: split
// into words, and otherwise every letter and digit kept.
func TestHandlesBecomeNames(t *testing.T) {
	for _, test := range []example{
		{"@EnkiKanataJellyus99", "Enki Kanata Jellyus 99"},
		{"@Reinzer-13", "Reinzer 13"},
		{"@Kiril_06", "Kiril 06"},
		{"@HaimiyaaMiooo.NReina", "Haimiyaa Miooo N Reina"},
		{"@jesssnuuu", "jesssnuuu"},
		{"@shirochantv", "shirochantv"},
		{"@Weeb-kunn", "Weeb kunn"},
		{"@12345", "12345"},
		{"Budi", "Budi"},
	} {
		if got := Name(test.written); got != test.spoken {
			t.Errorf("Name(%q) = %q, want %q", test.written, got, test.spoken)
		}
	}
}

// The number in a name is read out with the rest of it, as a number.
func TestTheNumberInANameIsSpoken(t *testing.T) {
	for _, test := range []example{
		{"@EnkiKanataJellyus99", "Enki Kanata Jellyus sembilan puluh sembilan"},
		{"@Reinzer-13", "Reinzer tiga belas"},
		{"@Kiril_06", "Kiril nol enam"},
	} {
		if got := Numbers(Name(test.written), "id"); got != test.spoken {
			t.Errorf("Numbers(Name(%q)) = %q, want %q", test.written, got, test.spoken)
		}
	}
}
