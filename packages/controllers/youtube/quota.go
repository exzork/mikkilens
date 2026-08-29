package youtube

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Quota is tracked locally rather than discovered by hitting the limit.
//
// The default allowance is 10,000 units a day and a single write costs 50, so
// a long stream can plausibly exhaust it. Running out mid-stream would stop
// chat dead, and stopping without saying so is the one failure mode this app
// must not have -- so the budget is counted here and warned about early.

// Costs are the published YouTube quota prices. Reads are cheap; anything that
// writes costs 50.
var Costs = map[string]int{
	"liveBroadcasts.list":       1,
	"liveBroadcasts.update":     50,
	"liveBroadcasts.transition": 50,
	"liveChatMessages.list":     5,
	"liveChatMessages.insert":   50,
	"videos.list":               1,
	"videos.update":             50,
	"channels.list":             1,
}

// Ledger counts the units spent today.
type Ledger struct {
	mu          sync.Mutex
	budget      int
	warnPercent int
	used        int
	day         string
}

type storedLedger struct {
	Day  string `json:"day"`
	Used int    `json:"used"`
}

// NewLedger builds a ledger, restoring today's usage if there is any.
func NewLedger(budget, warnPercent int) *Ledger {
	if budget <= 0 {
		budget = 10000
	}
	if warnPercent <= 0 {
		warnPercent = 80
	}
	ledger := &Ledger{budget: budget, warnPercent: warnPercent, day: today()}

	data, err := os.ReadFile(paths.QuotaFile())
	if err != nil {
		return ledger
	}
	var stored storedLedger
	if err := json.Unmarshal(data, &stored); err != nil {
		slog.Debug("could not read the quota ledger; starting fresh", "error", err)
		return ledger
	}
	// A ledger from another day is not this day's usage.
	if stored.Day == ledger.day {
		ledger.used = stored.Used
	}
	return ledger
}

func today() string { return time.Now().Format("2006-01-02") }

// Spend records the cost of one call and returns the new total.
func (l *Ledger) Spend(method string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rollOverLocked()
	cost, ok := Costs[method]
	if !ok {
		cost = 1
	}
	l.used += cost
	l.saveLocked()
	return l.used
}

// rollOverLocked resets the count when the day changes.
func (l *Ledger) rollOverLocked() {
	if current := today(); current != l.day {
		l.day, l.used = current, 0
	}
}

func (l *Ledger) saveLocked() {
	if _, err := paths.EnsureDataDir(); err != nil {
		return
	}
	encoded, err := json.Marshal(storedLedger{Day: l.day, Used: l.used})
	if err != nil {
		return
	}
	_ = os.WriteFile(paths.QuotaFile(), encoded, 0o644)
}

// Save writes the ledger out.
func (l *Ledger) Save() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.saveLocked()
}

// Used is how many units have gone today.
func (l *Ledger) Used() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

// Budget is the daily allowance.
func (l *Ledger) Budget() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.budget
}

// Day is the date this count belongs to.
func (l *Ledger) Day() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.day
}

// Percent is how much of the budget has gone, capped at 100.
func (l *Ledger) Percent() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.percentLocked()
}

func (l *Ledger) percentLocked() int {
	budget := l.budget
	if budget < 1 {
		budget = 1
	}
	percent := int(float64(100*l.used)/float64(budget) + 0.5)
	return min(100, percent)
}

// Exhausted reports whether the budget is spent.
func (l *Ledger) Exhausted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used >= l.budget
}

// ShouldWarn reports whether it is time to say the quota is running low.
func (l *Ledger) ShouldWarn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.percentLocked() >= l.warnPercent
}

// MarkExhausted records that the provider itself said the quota is gone, so
// nothing else tries and fails for the rest of the day.
func (l *Ledger) MarkExhausted() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.used = l.budget
	l.saveLocked()
}

// SetDay is used by tests to pretend the stored count is from another day.
func (l *Ledger) SetDay(day string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.day = day
}
