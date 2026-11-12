package voice

// transcriptItems tracks provider utterances in arrival order for providers
// that report results per conversation item. Only the read loop touches it.
type transcriptItems struct {
	items []*transcriptItem
	byID  map[string]*transcriptItem
}

type transcriptItem struct {
	text  string
	final bool
}

const liveItemID = "live"

func newTranscriptItems() *transcriptItems {
	return &transcriptItems{byID: map[string]*transcriptItem{}}
}

func (t *transcriptItems) get(id string) *transcriptItem {
	id = firstNonEmpty(id, liveItemID)
	if item, ok := t.byID[id]; ok {
		return item
	}
	item := &transcriptItem{}
	t.byID[id] = item
	t.items = append(t.items, item)
	return item
}

func (t *transcriptItems) allFinal() bool {
	if len(t.items) == 0 {
		return false
	}
	for _, item := range t.items {
		if !item.final {
			return false
		}
	}
	return true
}

// snapshot keeps transcript order: only the leading run of final items is
// final, everything after the first pending item is partial.
func (t *transcriptItems) snapshot() Snapshot {
	var final, partial []string
	for _, item := range t.items {
		if item.final && len(partial) == 0 {
			final = append(final, item.text)
		} else {
			partial = append(partial, item.text)
		}
	}
	return Snapshot{Final: joinText(final...), Partial: joinText(partial...)}
}
