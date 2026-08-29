package feedback

// queued is one entry in the speech queue.
//
// The sequence number is what keeps equal priorities in arrival order: a heap
// on its own gives no such promise, and chat read out of order would be worse
// than chat read late.
type queued struct {
	priority int
	sequence int
	what     Utterance
}

type utteranceHeap []queued

func (h utteranceHeap) Len() int { return len(h) }

func (h utteranceHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	return h[i].sequence < h[j].sequence
}

func (h utteranceHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *utteranceHeap) Push(value any) { *h = append(*h, value.(queued)) }

func (h *utteranceHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	*h = old[:last]
	return entry
}
