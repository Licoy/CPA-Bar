package usage

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealCPA subscribes to a running CPA: CPABAR_IT_ADDR=127.0.0.1:18317
// CPABAR_IT_KEY=<management key> [CPABAR_IT_CLIENT_KEY=<api key>].
func TestRealCPA(t *testing.T) {
	addr, key := os.Getenv("CPABAR_IT_ADDR"), os.Getenv("CPABAR_IT_KEY")
	if addr == "" {
		t.Skip("CPABAR_IT_ADDR not set")
	}
	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	c := clientFor(t, addr, key)
	m.Sync([]Target{{ID: "it", Client: c}})
	waitUntil(t, "live", func() bool {
		s := m.Status("it")
		if s.Err != nil {
			t.Logf("status %v: %v", s.State, s.Err)
		}
		return s.State == StateLive
	})
	waitUntil(t, "stats flag", func() bool { return m.Status("it").StatsEnabled != nil })
	t.Logf("usage-statistics-enabled = %v", *m.Status("it").StatsEnabled)

	if ck := os.Getenv("CPABAR_IT_CLIENT_KEY"); ck != "" {
		req, _ := http.NewRequest("POST", "http://"+addr+"/v1/chat/completions", strings.NewReader(`{"model":"fake","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+ck)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			t.Logf("request: %s", resp.Status)
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
		t.Logf("received %d records", m.Status("it").Received)
		m.Read("it", func(rs []Record) {
			for _, r := range rs {
				t.Logf("%+v", r)
			}
		})
	}
	// A wrong key is refused once and not retried.
	bad := clientFor(t, addr, "wrong-key")
	m.Sync([]Target{{ID: "it", Client: bad}})
	waitUntil(t, "refusal", func() bool { return m.Status("it").State == StateRejected })
	t.Logf("refused: %v", m.Status("it").Err)
}
