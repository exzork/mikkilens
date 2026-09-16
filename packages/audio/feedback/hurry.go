package feedback

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Reading chat faster while there is a backlog, and no faster than that.
//
// Chat arrives in bursts and is read one message at a time, so a busy minute
// leaves the reading behind. Dropping the oldest is one answer and the reader
// has it; this is the gentler one that comes first -- read what is waiting a
// little quicker, and the backlog clears without anybody's message being
// thrown away.
//
// It is a multiplier on the rate she configured rather than a replacement for
// it: chat is already read faster than the rest by default, and somebody who
// moved that number meant it, so the preference survives being hurried.

// maxChatSpeed is as fast as chat is ever read, however far behind it gets.
//
// Eighty percent faster is about where words stop being heard in passing and
// start needing attention, which is the thing being protected: chat is read so
// she can keep half an ear on it while doing something else. The audio itself
// holds up past this -- the overlaps that speed it up stay aligned to twice
// speed -- so what this number is about is how fast anybody can follow, not
// what the voice can manage. Falling further behind is answered by skipping
// instead, which costs one message rather than every message's clarity.
const maxChatSpeed = 1.8

// SetChatHurry sets how much faster than the rate she configured chat is read,
// as a multiplier of one. Anything at or below one reads at that rate.
func (b *Bus) SetChatHurry(hurry float32) {
	if hurry < 1 || math.IsNaN(float64(hurry)) {
		hurry = 1
	}
	b.mu.Lock()
	b.chatHurry = hurry
	b.mu.Unlock()
}

// chatRate is the rate one chat message is read at: what she configured,
// multiplied by however much of a hurry the reading is in, held at
// maxChatSpeed.
func (b *Bus) chatRate(configured string) string {
	b.mu.Lock()
	hurry := b.chatHurry
	b.mu.Unlock()

	if hurry <= 1.001 {
		return configured
	}
	speed := speedOfRate(configured) * hurry
	if speed > maxChatSpeed {
		speed = maxChatSpeed
	}
	return fmt.Sprintf("%+d%%", int(math.Round(float64(speed-1)*100)))
}

// speedOfRate turns "+15%" into 1.15. Every engine reads the string this way;
// this is the one place the bus needs the number rather than the words.
func speedOfRate(rate string) float32 {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rate), "%"))
	if trimmed == "" {
		return 1
	}
	value, err := strconv.ParseFloat(trimmed, 32)
	if err != nil {
		return 1
	}
	return 1 + float32(value)/100
}
