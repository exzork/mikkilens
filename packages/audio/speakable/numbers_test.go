package speakable

import "testing"

type example struct{ written, spoken string }

func check(t *testing.T, language string, examples []example) {
	t.Helper()
	for _, test := range examples {
		if got := Numbers(test.written, language); got != test.spoken {
			t.Errorf("Numbers(%q, %q)\n got %q\nwant %q", test.written, language, got, test.spoken)
		}
	}
}

func TestIndonesianNumbers(t *testing.T) {
	check(t, "id", []example{
		{"Ada 5 chat.", "Ada lima chat."},
		{"Sekarang sudah 12.500 subscriber, waktu debut jumlahnya baru 340.",
			"Sekarang sudah dua belas ribu lima ratus subscriber, waktu debut jumlahnya baru tiga ratus empat puluh."},
		{"Penonton 1500 orang.", "Penonton seribu lima ratus orang."},
		{"0", "nol"},
		{"10 11 12 19 20 21", "sepuluh sebelas dua belas sembilan belas dua puluh dua puluh satu"},
		{"100 111 1001 2026", "seratus seratus sebelas seribu satu dua ribu dua puluh enam"},
		{"1.000.000", "satu juta"},
		{"3,5", "tiga koma lima"},
		{"versi 1.2.3", "versi satu titik dua titik tiga"},
		{"1. Lagu", "satu. Lagu"},
		{"suhu -5 derajat", "suhu minus lima derajat"},
		{"COVID-19", "COVID-sembilan belas"},
		{"lagu mp3", "lagu mp tiga"},
		{"08123456789", "nol delapan satu dua tiga empat lima enam tujuh delapan sembilan"},
	})
}

// A donation read as the wrong amount is the one mistake here that costs her
// something, so every shape an amount arrives in is pinned.
func TestIndonesianMoney(t *testing.T) {
	check(t, "id", []example{
		{"Donasi dari Budi, Rp50.000.", "Donasi dari Budi, lima puluh ribu rupiah."},
		{"Rp 5.000", "lima ribu rupiah"},
		{"Rp50.000,00", "lima puluh ribu rupiah"},
		{"IDR 50,000", "lima puluh ribu rupiah"},
		{"Rp10rb", "sepuluh ribu rupiah"},
		{"25 USD", "dua puluh lima dolar"},
		{"$12.25", "dua belas dolar dua puluh lima sen"},
		{"10k", "sepuluh ribu"},
		{"1,5jt", "satu juta lima ratus ribu"},
		{"Volume 50%", "Volume lima puluh persen"},
	})
}

func TestIndonesianTimesDatesAndOrder(t *testing.T) {
	check(t, "id", []example{
		{"Sekarang jam 20:15.", "Sekarang jam dua puluh lewat lima belas menit."},
		{"Sekarang jam 20:00.", "Sekarang jam dua puluh."},
		{"pukul 20.00 waktu Indonesia Barat", "pukul dua puluh waktu Indonesia Barat"},
		{"12/09/2026", "dua belas September dua ribu dua puluh enam"},
		{"2026-09-15", "lima belas September dua ribu dua puluh enam"},
		{"lagu ke-3", "lagu ketiga"},
		{"yang ke-1", "yang pertama"},
		{"nomor 1-3", "nomor satu sampai tiga"},
		{"2x", "dua kali"},
	})
}

func TestEnglishNumbers(t *testing.T) {
	check(t, "en", []example{
		{"12,500 viewers.", "twelve thousand five hundred viewers."},
		{"105 2026 21", "one hundred five two thousand twenty-six twenty-one"},
		{"1,000,000", "one million"},
		{"3.5", "three point five"},
		{"-5", "minus five"},
		{"version 1.2.3", "version one point two point three"},
		{"50%", "fifty percent"},
		{"10k", "ten thousand"},
		{"1-3", "one to three"},
		{"2x", "two times"},
	})
}

func TestEnglishMoney(t *testing.T) {
	check(t, "en", []example{
		{"$1", "one dollar"},
		{"$5", "five dollars"},
		{"$12.25", "twelve dollars and twenty-five cents"},
		{"Rp50.000", "fifty thousand rupiah"},
		{"IDR 50,000", "fifty thousand rupiah"},
		{"25 USD", "twenty-five dollars"},
	})
}

func TestEnglishTimesDatesAndOrder(t *testing.T) {
	check(t, "en", []example{
		{"It is 20:15.", "It is twenty fifteen."},
		{"It is 20:00.", "It is twenty o'clock."},
		{"It is 08:05.", "It is eight oh five."},
		{"at 8.30", "at eight thirty"},
		{"12/09/2026", "the twelfth of September two thousand twenty-six"},
		{"1st 2nd 3rd 12th 20th 21st", "first second third twelfth twentieth twenty-first"},
	})
}

// A language MikkiLens does not speak keeps its digits: the voice reading them
// its own way beats them spelled out in Indonesian.
func TestOtherLanguagesAreLeftAlone(t *testing.T) {
	for _, language := range []string{"ja", "", "xx"} {
		if got := Numbers("Rp50.000 20:15", language); got != "Rp50.000 20:15" {
			t.Errorf("Numbers(_, %q) = %q", language, got)
		}
	}
	if got := Numbers("Selamat datang", "id"); got != "Selamat datang" {
		t.Errorf("text without numbers changed: %q", got)
	}
	if got := Numbers("Ada 5 chat.", "id-ID"); got != "Ada lima chat." {
		t.Errorf("a full locale is not read as its language: %q", got)
	}
}
