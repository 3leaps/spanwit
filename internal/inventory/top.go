package inventory

import "container/heap"

type topHeap struct {
	limit int
	basis string
	items entryHeap
}

func newTopHeap(limit int, basis string) *topHeap {
	h := &topHeap{limit: limit, basis: basis}
	h.items.basis = basis
	heap.Init(&h.items)
	return h
}

func (h *topHeap) add(entry Entry) {
	if h.limit <= 0 {
		return
	}
	if h.items.Len() < h.limit {
		heap.Push(&h.items, entry)
		return
	}
	if entryLess(entry, h.items.entries[0], h.basis) {
		h.items.entries[0] = entry
		heap.Fix(&h.items, 0)
	}
}

func (h *topHeap) entries() []Entry {
	out := append([]Entry(nil), h.items.entries...)
	sortEntries(out, h.basis)
	return out
}

type entryHeap struct {
	basis   string
	entries []Entry
}

func (h entryHeap) Len() int { return len(h.entries) }

// Less places the worst retained entry at index zero: smaller selected size,
// then lexicographically later identity for deterministic ties.
func (h entryHeap) Less(i, j int) bool {
	is, _ := h.entries[i].SelectedSize(h.basis)
	js, _ := h.entries[j].SelectedSize(h.basis)
	if is != js {
		return is < js
	}
	if h.entries[i].RootID != h.entries[j].RootID {
		return h.entries[i].RootID > h.entries[j].RootID
	}
	return h.entries[i].RelativePath > h.entries[j].RelativePath
}

func (h entryHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
}

func (h *entryHeap) Push(value any) {
	h.entries = append(h.entries, value.(Entry))
}

func (h *entryHeap) Pop() any {
	old := h.entries
	n := len(old)
	value := old[n-1]
	h.entries = old[:n-1]
	return value
}
