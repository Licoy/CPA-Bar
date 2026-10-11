package core

import "testing"

func TestAppendSampleKeepsLatest(t *testing.T) {
	var h []Sample
	for i := range HistoryLen + 5 {
		prev := h
		h = appendSample(h, Sample{Accounts: AccountSummary{Total: i}})
		if len(prev) > 0 && &prev[0] == &h[0] {
			t.Fatal("the history was changed in place")
		}
	}
	if len(h) != HistoryLen {
		t.Fatalf("len %d, want %d", len(h), HistoryLen)
	}
	if h[0].Accounts.Total != 5 || h[len(h)-1].Accounts.Total != HistoryLen+4 {
		t.Errorf("kept %d..%d", h[0].Accounts.Total, h[len(h)-1].Accounts.Total)
	}
}
