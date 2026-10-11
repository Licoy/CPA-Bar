package usage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/cpamp"
)

// An instance with CPA-Manager-Plus in front of it has its records read
// from there rather than from CPA's stream: Manager-Plus keeps every record
// since it was set up, including those of the time CPA-Bar was not running.
// Subscribing to CPA's stream as well would be no use, and while
// Manager-Plus takes records off CPA's queue, a subscriber makes CPA hand
// them to the subscriber instead, which would starve Manager-Plus.

// plusEvery is how often Manager-Plus is read; tests shorten it.
var plusEvery = 15 * time.Second

const (
	// plusOverlap reads again the last minutes, for the records
	// Manager-Plus collects late.
	plusOverlap = 5 * time.Minute
	// plusMaxPages bounds the pages of one read.
	plusMaxPages = 60
)

// FromManagerPlus turns an event of CPA-Manager-Plus into a record, keeping
// what Parse keeps.
func FromManagerPlus(e cpamp.Event) (Record, bool) {
	id := strings.TrimSpace(e.RequestID)
	if id == "" && e.EventHash != "" {
		id = "mp:" + e.EventHash
	}
	if id == "" || e.TimestampMS <= 0 {
		return Record{}, false
	}
	r := Record{
		Time:          time.UnixMilli(e.TimestampMS),
		RequestID:     id,
		Provider:      strings.TrimSpace(e.AuthProviderSnapshot),
		Executor:      e.ExecutorType,
		Model:         strings.TrimSpace(cmp.Or(e.ResolvedModel, e.Model)),
		ResponseModel: strings.TrimSpace(e.ResponseModel),
		Endpoint:      cmp.Or(e.Endpoint, strings.TrimSpace(e.Method+" "+e.Path)),
		AuthIndex:     e.AuthIndex,
		Source:        source(cmp.Or(e.AccountSnapshot, e.Source), ""),
		Failed:        e.Failed,
		Stream:        e.Stream != nil && *e.Stream,
		Reasoning:     e.ReasoningEffort,
		ServiceTier:   e.ServiceTier,
	}
	if a := strings.TrimSpace(e.RequestedModel); a != "" && a != r.Model {
		r.Alias = a
	}
	if r.ResponseModel == r.Model {
		r.ResponseModel = ""
	}
	if e.LatencyMS != nil {
		r.LatencyMs = max(0, *e.LatencyMS)
	}
	if e.TTFTMS != nil {
		r.TTFTMs = max(0, *e.TTFTMS)
	}
	// Manager-Plus keeps a SHA-256 of the client's key; the fingerprint is
	// its start, as for the stream's records.
	if h := strings.ToLower(strings.TrimSpace(e.APIKeyHash)); len(h) >= 12 {
		r.KeyID = h[:12]
	}
	if e.Failed {
		if e.FailStatusCode != nil {
			r.Status = int(*e.FailStatusCode)
		}
		r.Error = failBody(json.RawMessage(quote(e.FailSummary)))
		low := strings.ToLower(r.Error)
		r.Canceled = r.Status == 499 || strings.Contains(low, "context canceled") || strings.Contains(low, "client closed request")
	}
	r.Tokens = Tokens{
		Input:       max(0, e.InputTokens),
		Output:      max(0, e.OutputTokens),
		Reasoning:   max(0, e.ReasoningTokens),
		CacheRead:   max(0, cmp.Or(e.CacheReadTokens, e.CachedTokens)),
		CacheCreate: max(0, e.CacheCreationTokens),
		Total:       max(0, e.TotalTokens),
	}
	if r.Tokens.Total == 0 {
		r.Tokens.Total = r.Tokens.Input + r.Tokens.Output
	}
	return r, true
}

func quote(s string) string {
	if s == "" {
		return ""
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// runPlus reads the records of one instance from Manager-Plus until ctx
// ends. c, CPA's client, names the client keys of the records.
func (m *Manager) runPlus(ctx context.Context, id string, p *cpamp.Client, c *cpa.Client) {
	m.st.read(id, func([]Record) {}) // load the file here, not on the UI thread
	masks := &keyMasks{c: c}
	wait := retryMin
	for {
		n, err := m.readPlus(ctx, id, p, masks)
		if ctx.Err() != nil {
			return
		}
		now := m.now()
		state, delay := StateLive, plusEvery
		switch {
		case cpamp.IsUnauthorized(err):
			state = StateRejected
		case err != nil:
			state, delay = StateRetrying, wait
			wait = min(wait*2, retryMax)
		default:
			wait = retryMin
		}
		m.setStatus(ctx, id, func(s *Status) {
			if s.State != state {
				s.Since = now
			}
			s.State, s.Err, s.Retry = state, err, time.Time{}
			if state == StateRetrying {
				s.Retry = now.Add(delay)
			}
			if err == nil {
				s.Received += n
				s.Synced = now
			}
		})
		if state == StateRejected {
			return // until the key changes
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// readPlus adds the records Manager-Plus has since the last kept one (the
// retention when there is none), less the overlap, that are not kept yet.
// It reports how many it added.
func (m *Manager) readPlus(ctx context.Context, id string, p *cpamp.Client, masks *keyMasks) (int, error) {
	now := m.now()
	from := now.Add(-Retention)
	seen := map[string]bool{}
	m.st.read(id, func(recs []Record) {
		if n := len(recs); n > 0 {
			if t := recs[n-1].Time.Add(-plusOverlap); t.After(from) {
				from = t
			}
		}
		for i := len(recs) - 1; i >= 0 && !recs[i].Time.Before(from); i-- {
			seen[recs[i].RequestID] = true
		}
	})
	var got []Record
	var beforeMS, beforeID int64
	for range plusMaxPages {
		page, err := p.Events(ctx, from, now.Add(time.Minute), beforeMS, beforeID, cpamp.MaxPage)
		if err != nil {
			return 0, err
		}
		for _, e := range page.Items {
			if r, ok := FromManagerPlus(e); ok && !seen[r.RequestID] {
				seen[r.RequestID] = true
				got = append(got, r)
			}
		}
		if !page.HasMore || len(page.Items) == 0 {
			break
		}
		beforeMS, beforeID = page.NextBeforeMS, page.NextBeforeID
	}
	if len(got) == 0 {
		return 0, nil
	}
	// Pages come newest first; the store takes them oldest first.
	slices.Reverse(got)
	for i := range got {
		got[i].KeyMask = masks.mask(ctx, got[i].KeyID, now)
	}
	if err := m.st.add(id, got...); err != nil {
		return 0, err
	}
	m.notify(false)
	return len(got), nil
}

// keyMasks names fingerprints of client keys with the masked form of CPA's
// client keys (access.api-keys), read again at most every few minutes
// when a fingerprint is unknown. The keys themselves are dropped at once.
// A refusal of CPA's key stops the reads: every refusal counts towards a
// ban.
type keyMasks struct {
	c       *cpa.Client
	byID    map[string]string
	read    time.Time
	refused bool
}

const keyMasksEvery = 5 * time.Minute

func (k *keyMasks) mask(ctx context.Context, id string, now time.Time) string {
	if id == "" {
		return ""
	}
	if m, ok := k.byID[id]; ok {
		return m
	}
	if k.c == nil || k.refused || (!k.read.IsZero() && now.Sub(k.read) < keyMasksEvery) {
		return ""
	}
	k.read = now
	raw, err := k.c.GetConfig(ctx, "access", "api-keys")
	var ae *cpa.APIError
	if errors.As(err, &ae) && ae.IsAuth() {
		k.refused = true
	}
	var keys []string
	if err != nil || json.Unmarshal(raw, &keys) != nil {
		return ""
	}
	k.byID = map[string]string{}
	for _, key := range keys {
		if fid, m := keyFingerprint(key); fid != "" {
			k.byID[fid] = m
		}
	}
	return k.byID[id]
}
