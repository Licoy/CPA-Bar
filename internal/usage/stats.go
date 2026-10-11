package usage

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Result filters records by outcome.
type Result int

const (
	ResultAll Result = iota
	ResultSuccess
	ResultFailed
	ResultCanceled
)

// Filter selects records. Empty fields match everything; From and To bound
// the time, zero for open.
type Filter struct {
	From, To time.Time
	Model    string
	Provider string
	Source   string
	AuthIdx  string
	KeyID    string
	Result   Result
}

func (f Filter) match(r Record) bool {
	switch {
	case !f.From.IsZero() && r.Time.Before(f.From),
		!f.To.IsZero() && r.Time.After(f.To),
		f.Model != "" && r.DisplayModel() != f.Model,
		f.Provider != "" && !strings.EqualFold(r.Provider, f.Provider),
		f.Source != "" && r.Source != f.Source,
		f.AuthIdx != "" && r.AuthIndex != f.AuthIdx,
		f.KeyID != "" && r.KeyID != f.KeyID:
		return false
	}
	switch f.Result {
	case ResultSuccess:
		return !r.Failed
	case ResultFailed:
		return r.Failed && !r.Canceled
	case ResultCanceled:
		return r.Canceled
	}
	return true
}

// Group sums the records that share a model, a provider, an account or a
// key.
type Group struct {
	Key string
	// Label is how to show the key when it is not the key itself (the
	// masked API key).
	Label string
	// Provider is the provider of the group's records, for an account:
	// the first one that names it.
	Provider string
	Requests int
	Failed   int
	Tokens   Tokens
}

// Bucket sums the records of one time slot.
type Bucket struct {
	Start    time.Time
	Requests int
	Failed   int
	Tokens   int64
	// ByModel splits Tokens over the top models of the summary, in the
	// order of Summary.TopModels; the last entry is the rest.
	ByModel []int64
}

// Summary sums the records a filter selects.
type Summary struct {
	Requests int
	Success  int
	Failed   int // failed, canceled excluded
	Canceled int
	Tokens   Tokens

	LatencySum int64
	TTFTSum    int64
	TTFTCount  int
	// TPS is output tokens per second over the requests that generated
	// some.
	tpsOut, tpsMs int64

	First, Last time.Time

	Models    []Group
	Providers []Group
	Sources   []Group
	Keys      []Group
	// Options are every model, provider, account and key in the time
	// range, whatever the other filters, for the filter menus.
	Options Choices

	Buckets   []Bucket
	TopModels []string
}

// Choices are the values the filters can take.
type Choices struct {
	Models, Providers, Sources []string
	Keys                       []Group // Key and Label
}

// SuccessRate is success over success and failure, canceled requests left
// out; ok is false when there are none.
func (s Summary) SuccessRate() (rate float64, ok bool) {
	n := s.Success + s.Failed
	if n == 0 {
		return 0, false
	}
	return float64(s.Success) / float64(n), true
}

// CacheRate is the share of the input read from the cache.
func (s Summary) CacheRate() (float64, bool) {
	if s.Tokens.Input == 0 {
		return 0, false
	}
	return min(1, float64(s.Tokens.CacheRead)/float64(s.Tokens.Input)), true
}

// AvgLatency is the mean latency of all requests.
func (s Summary) AvgLatency() (time.Duration, bool) {
	if s.Requests == 0 {
		return 0, false
	}
	return time.Duration(s.LatencySum/int64(s.Requests)) * time.Millisecond, true
}

// AvgTTFT is the mean time to the first token of the requests that report
// one.
func (s Summary) AvgTTFT() (time.Duration, bool) {
	if s.TTFTCount == 0 {
		return 0, false
	}
	return time.Duration(s.TTFTSum/int64(s.TTFTCount)) * time.Millisecond, true
}

// TPS is output tokens per second of generation.
func (s Summary) TPS() (float64, bool) {
	if s.tpsMs == 0 {
		return 0, false
	}
	return float64(s.tpsOut) * 1000 / float64(s.tpsMs), true
}

// RPM is requests per minute over the range, or over the records when the
// range is open.
func (s Summary) RPM(from, to time.Time) float64 {
	if s.Requests == 0 {
		return 0
	}
	if from.IsZero() {
		from = s.First
	}
	if to.IsZero() {
		to = s.Last
	}
	return float64(s.Requests) / max(1, to.Sub(from).Minutes())
}

// topModels is how many models the trend splits out.
const topModels = 6

// Summarize sums the records f selects, with the trend in slots of step
// from f.From (or the first record) to f.To (or now).
func Summarize(recs []Record, f Filter, step time.Duration, now time.Time) Summary {
	var s Summary
	models := map[string]*Group{}
	providers := map[string]*Group{}
	sources := map[string]*Group{}
	keys := map[string]*Group{}
	opt := struct{ models, providers, sources, keys map[string]string }{map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}}
	inRange := Filter{From: f.From, To: f.To}

	// Records are sorted by time: start at the first in range.
	start := 0
	if !f.From.IsZero() {
		start, _ = slices.BinarySearchFunc(recs, f.From, func(r Record, t time.Time) int { return r.Time.Compare(t) })
	}
	var sel []int
	for i := start; i < len(recs); i++ {
		r := recs[i]
		if !f.To.IsZero() && r.Time.After(f.To) {
			break
		}
		if !inRange.match(r) {
			continue
		}
		opt.models[r.DisplayModel()] = ""
		if r.Provider != "" {
			opt.providers[r.Provider] = ""
		}
		if r.Source != "" {
			opt.sources[r.Source] = ""
		}
		if r.KeyID != "" {
			opt.keys[r.KeyID] = r.KeyMask
		}
		if !f.match(r) {
			continue
		}
		sel = append(sel, i)
		s.Requests++
		switch {
		case r.Canceled:
			s.Canceled++
		case r.Failed:
			s.Failed++
		default:
			s.Success++
		}
		s.Tokens.add(r.Tokens)
		s.LatencySum += r.LatencyMs
		if r.TTFTMs > 0 {
			s.TTFTSum += r.TTFTMs
			s.TTFTCount++
		}
		if !r.Failed && r.Tokens.Output > 0 && r.LatencyMs > 0 {
			s.tpsOut += r.Tokens.Output
			s.tpsMs += r.LatencyMs
		}
		if s.First.IsZero() {
			s.First = r.Time
		}
		s.Last = r.Time
		addTo(models, r.DisplayModel(), "", r)
		addTo(providers, cmp.Or(r.Provider, "unknown"), "", r)
		if r.Source != "" {
			addTo(sources, r.Source, "", r)
		}
		if r.KeyID != "" {
			addTo(keys, r.KeyID, r.KeyMask, r)
		}
	}
	s.Models = sorted(models)
	s.Providers = sorted(providers)
	s.Sources = sorted(sources)
	s.Keys = sorted(keys)
	s.Options = Choices{Models: sortedKeys(opt.models), Providers: sortedKeys(opt.providers), Sources: sortedKeys(opt.sources)}
	for _, k := range sortedKeys(opt.keys) {
		s.Options.Keys = append(s.Options.Keys, Group{Key: k, Label: opt.keys[k]})
	}

	for i, g := range s.Models {
		if i == topModels {
			break
		}
		s.TopModels = append(s.TopModels, g.Key)
	}
	s.Buckets = buckets(recs, sel, s.TopModels, f, step, s.First, now)
	return s
}

func addTo(m map[string]*Group, key, label string, r Record) {
	g, ok := m[key]
	if !ok {
		g = &Group{Key: key, Label: label}
		m[key] = g
	}
	if g.Provider == "" {
		g.Provider = r.Provider
	}
	g.Requests++
	if r.Failed && !r.Canceled {
		g.Failed++
	}
	g.Tokens.add(r.Tokens)
}

// sorted orders groups by tokens, then requests, then key.
func sorted(m map[string]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b Group) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total, a.Tokens.Total), cmp.Compare(b.Requests, a.Requests), strings.Compare(a.Key, b.Key))
	})
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// maxBuckets bounds the slots of a trend.
const maxBuckets = 400

func buckets(recs []Record, sel []int, top []string, f Filter, step time.Duration, first, now time.Time) []Bucket {
	if step <= 0 {
		return nil
	}
	from, to := f.From, f.To
	if from.IsZero() {
		if first.IsZero() {
			return nil
		}
		from = first
	}
	if to.IsZero() {
		to = now
	}
	from = slot(from, step)
	if !to.After(from) {
		to = from.Add(step)
	}
	n := int(to.Sub(from)/step) + 1
	if n > maxBuckets {
		n = maxBuckets
		from = slot(to, step).Add(-time.Duration(n-1) * step)
	}
	out := make([]Bucket, n)
	for i := range out {
		out[i].Start = from.Add(time.Duration(i) * step)
		out[i].ByModel = make([]int64, len(top)+1)
	}
	for _, i := range sel {
		r := recs[i]
		k := int(r.Time.Sub(from) / step)
		if k < 0 || k >= n {
			continue
		}
		b := &out[k]
		b.Requests++
		if r.Failed && !r.Canceled {
			b.Failed++
		}
		b.Tokens += r.Tokens.Total
		j := slices.Index(top, r.DisplayModel())
		if j < 0 {
			j = len(top)
		}
		b.ByModel[j] += r.Tokens.Total
	}
	return out
}

// slot is the start of the slot holding t, in local time for slots of a
// day or more.
func slot(t time.Time, step time.Duration) time.Time {
	if step >= 24*time.Hour {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	}
	return t.Truncate(step)
}

// Recent returns up to limit records f selects after skipping offset,
// newest first, and how many there are in all.
func Recent(recs []Record, f Filter, offset, limit int) ([]Record, int) {
	var out []Record
	total := 0
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if !f.From.IsZero() && r.Time.Before(f.From) {
			break
		}
		if !f.match(r) {
			continue
		}
		if total >= offset && len(out) < limit {
			out = append(out, r)
		}
		total++
	}
	return out, total
}
