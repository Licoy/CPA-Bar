package view

import (
	"testing"
	"time"
)

func TestClampPanelHeight(t *testing.T) {
	for _, tc := range []struct{ h, limit, want int }{
		{0, 0, panelHeight},
		{0, 1000, panelHeight},
		{0, 400, 400}, // a short screen shrinks the default
		{100, 1000, panelMinHeight},
		{900, 1000, 900},
		{1200, 1000, 1000},
		{1200, 0, 1200}, // no screen known
	} {
		if got := clampPanelHeight(tc.h, tc.limit); got != tc.want {
			t.Errorf("clampPanelHeight(%d, %d) = %d, want %d", tc.h, tc.limit, got, tc.want)
		}
	}
	if got := panelHeightLimit(1080); got != 864 {
		t.Errorf("limit of 1080 = %d, want 864", got)
	}
	if got := panelHeightLimit(0); got != 0 {
		t.Errorf("limit of unknown screen = %d", got)
	}
}

func TestTogglePanelIgnoresClickThatClosedIt(t *testing.T) {
	// The press of this click closed the panel: its release does not
	// open it again (a nil Svc would panic in OpenPanel).
	a := &App{panelClosedAt: time.Now()}
	a.TogglePanel()
	if a.panel != nil || !a.panelClosedAt.IsZero() {
		t.Fatalf("panel = %v, closedAt = %v; want the click ignored once", a.panel, a.panelClosedAt)
	}
}

func TestClosePanelDoesNotHoldBackNextClick(t *testing.T) {
	// Closing the panel from the app, as the manager or the tray's menu
	// do, is no press on the icon whose release is to be ignored.
	a := &App{panelClosedAt: time.Now()}
	a.ClosePanel()
	if !a.panelClosedAt.IsZero() {
		t.Fatalf("closedAt = %v after ClosePanel, want zero", a.panelClosedAt)
	}
}
