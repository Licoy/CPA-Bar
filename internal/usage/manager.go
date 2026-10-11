package usage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/cpamp"
)

// State is where the stream of an instance is.
type State int

const (
	// StateOff: collection is off, or the instance is paused or has no
	// key.
	StateOff State = iota
	StateConnecting
	StateLive
	// StateRetrying: the connection failed or dropped; it is tried again
	// after a while.
	StateRetrying
	// StateRejected: the server refused the key. It is not tried again
	// until the key or the address changes, since every refusal counts
	// towards a ban of this address.
	StateRejected
	// StateHomeMode: the server is in home mode, which has no usage
	// output.
	StateHomeMode
	// StateUnsupported: the address does not answer the stream protocol,
	// as with an HTTP-only reverse proxy. Tried again rarely.
	StateUnsupported
)

// Status is the stream of an instance.
type Status struct {
	State State
	Err   error
	// Since is when the state began; Retry when the next attempt is.
	Since, Retry time.Time
	// Received counts the records of this session.
	Received int
	Last     time.Time
	// StatsEnabled is whether the server records usage at all
	// (usage-statistics-enabled); nil while unknown.
	StatsEnabled *bool
	// ManagerPlus is whether the records are read from CPA-Manager-Plus
	// rather than from the stream; Synced is when they last were.
	ManagerPlus bool
	Synced      time.Time
}

// Target is an instance to collect from.
type Target struct {
	ID     string
	Client *cpa.Client
	// Plus, when set, is CPA-Manager-Plus in front of the instance: the
	// records are read from it, and CPA's stream is left alone.
	Plus *cpamp.Client
}

const (
	retryMin         = 2 * time.Second
	retryMax         = 2 * time.Minute
	retryUnsupported = 10 * time.Minute
	flushEvery       = 2 * time.Second
	// notifyEvery throttles the change notifications of busy streams.
	notifyEvery = time.Second
)

// Manager collects the usage of every instance it is given.
type Manager struct {
	st  *store
	now func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	enabled   bool
	subs      map[string]*sub
	status    map[string]*Status
	listeners map[int]func()
	nextL     int
	pending   bool // a notification is scheduled
}

type sub struct {
	client *cpa.Client
	plus   *cpamp.Client
	cancel context.CancelFunc
}

// Options configures a Manager.
type Options struct {
	// Dir holds the record files.
	Dir string
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// New makes a manager. It collects nothing until Sync is called.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		st:        newStore(o.Dir, o.Now),
		now:       o.Now,
		ctx:       ctx,
		cancel:    cancel,
		enabled:   true,
		subs:      map[string]*sub{},
		status:    map[string]*Status{},
		listeners: map[int]func(){},
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.st.flush()
			}
		}
	}()
	return m
}

// Close stops every stream and writes what is buffered.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
	m.st.flush()
	m.st.close()
}

// SetEnabled turns collection on or off for every instance. Sync applies
// it.
func (m *Manager) SetEnabled(on bool) {
	m.mu.Lock()
	m.enabled = on
	m.mu.Unlock()
}

// Enabled reports whether collection is on.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// Sync collects from exactly the targets given (none while collection is
// off). A target whose client changed (a new key or address) is
// reconnected.
func (m *Manager) Sync(targets []Target) {
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	want := map[string]Target{}
	if m.enabled {
		for _, t := range targets {
			if t.Client != nil || t.Plus != nil {
				want[t.ID] = t
			}
		}
	}
	changed := false
	for id, s := range m.subs {
		if t, ok := want[id]; !ok || t.Client != s.client || t.Plus != s.plus {
			s.cancel()
			delete(m.subs, id)
			m.status[id] = &Status{State: StateOff, Since: m.now()}
			changed = true
		}
	}
	for id, t := range want {
		if _, ok := m.subs[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.subs[id] = &sub{client: t.Client, plus: t.Plus, cancel: cancel}
		m.status[id] = &Status{State: StateConnecting, Since: m.now(), ManagerPlus: t.Plus != nil}
		changed = true
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if t.Plus != nil {
				m.runPlus(ctx, id, t.Plus, t.Client)
				return
			}
			m.run(ctx, id, t.Client)
		}()
	}
	known := map[string]bool{}
	for _, t := range targets {
		known[t.ID] = true
	}
	for id := range m.status {
		if !known[id] {
			delete(m.status, id) // a removed instance
		}
	}
	m.mu.Unlock()
	if changed {
		m.notify(true)
	}
}

// run keeps the stream of one instance open until ctx ends.
func (m *Manager) run(ctx context.Context, id string, c *cpa.Client) {
	m.st.read(id, func([]Record) {}) // load the file here, not on the UI thread
	wait := retryMin
	for {
		go m.checkStats(ctx, id, c)
		connected := time.Time{}
		err := c.SubscribeUsage(ctx, func() {
			connected = m.now()
			m.setStatus(ctx, id, func(s *Status) {
				s.State, s.Err, s.Since, s.Retry = StateLive, nil, connected, time.Time{}
			})
		}, func(msg []byte) {
			m.receive(ctx, id, msg)
		})
		if ctx.Err() != nil {
			return
		}
		if !connected.IsZero() && m.now().Sub(connected) > retryMax {
			wait = retryMin // it was up for a while: a fresh failure
		}
		state, delay := StateRetrying, wait
		var se *cpa.StreamError
		if errors.As(err, &se) {
			switch se.Reason {
			case cpa.StreamRejected:
				state = StateRejected
			case cpa.StreamHomeMode:
				state = StateHomeMode
			case cpa.StreamUnsupported:
				state, delay = StateUnsupported, retryUnsupported
			}
		}
		now := m.now()
		m.setStatus(ctx, id, func(s *Status) {
			s.State, s.Err, s.Since = state, err, now
			s.Retry = time.Time{}
			if state == StateRetrying || state == StateUnsupported {
				s.Retry = now.Add(delay)
			}
		})
		if state == StateRejected || state == StateHomeMode {
			return // until the client changes
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if state == StateRetrying {
			wait = min(wait*2, retryMax)
		}
		m.setStatus(ctx, id, func(s *Status) { s.State, s.Since = StateConnecting, m.now() })
	}
}

func (m *Manager) receive(ctx context.Context, id string, msg []byte) {
	r, ok := Parse(msg, m.now())
	if !ok {
		return
	}
	_ = m.st.add(id, r)
	m.mu.Lock()
	if s := m.status[id]; s != nil && ctx.Err() == nil {
		s.Received++
		s.Last = r.Time
	}
	m.mu.Unlock()
	m.notify(false)
}

// checkStats reads whether the server records usage.
func (m *Manager) checkStats(ctx context.Context, id string, c *cpa.Client) {
	on, err := statsEnabled(ctx, c)
	if err != nil {
		return
	}
	m.setStatus(ctx, id, func(s *Status) { s.StatsEnabled = &on })
}

// statsPath is where v8 keeps usage-statistics-enabled; legacy files have
// it at the top level.
var statsPath = []string{"observability", "usage", "usage-statistics-enabled"}

func statsEnabled(ctx context.Context, c *cpa.Client) (bool, error) {
	raw, err := c.GetConfig(ctx, statsPath...)
	var ae *cpa.APIError
	if errors.As(err, &ae) && ae.Kind == cpa.KindNotFound {
		raw, err = c.GetConfig(ctx, "usage-statistics-enabled")
		if errors.As(err, &ae) && ae.Kind == cpa.KindNotFound {
			return false, nil // not set: off
		}
	}
	if err != nil {
		return false, err
	}
	var on bool
	if err := json.Unmarshal(raw, &on); err != nil {
		return false, err
	}
	return on, nil
}

// EnableStats turns on usage-statistics-enabled on the server of a target.
// The write is sent once; an unknown outcome is returned as such.
func (m *Manager) EnableStats(ctx context.Context, id string, c *cpa.Client) error {
	if err := c.PutConfig(ctx, json.RawMessage("true"), statsPath...); err != nil {
		return err
	}
	on := true
	m.mu.Lock()
	if s := m.status[id]; s != nil {
		s.StatsEnabled = &on
	}
	m.mu.Unlock()
	m.notify(true)
	return nil
}

func (m *Manager) setStatus(ctx context.Context, id string, fn func(*Status)) {
	m.mu.Lock()
	s := m.status[id]
	if s == nil || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	fn(s)
	m.mu.Unlock()
	m.notify(true)
}

// Status is the stream of an instance.
func (m *Manager) Status(id string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.status[id]; s != nil {
		return *s
	}
	return Status{}
}

// Read calls fn with the records of an instance, oldest first. fn must not
// keep the slice nor take long: it holds up the collection.
func (m *Manager) Read(id string, fn func([]Record)) { m.st.read(id, fn) }

// Add keeps records of an instance as if they had arrived on its stream.
func (m *Manager) Add(id string, recs ...Record) error {
	err := m.st.add(id, recs...)
	m.notify(false)
	return err
}

// Version changes whenever the records of any instance change, for caching
// what is computed from them.
func (m *Manager) Version() uint64 { return m.st.ver.Load() }

// Clear deletes the records of an instance.
func (m *Manager) Clear(id string) error {
	err := m.st.clear(id)
	m.notify(true)
	return err
}

// Subscribe registers fn to run after changes; busy streams notify at most
// once a second. It returns a function that removes fn.
func (m *Manager) Subscribe(fn func()) func() {
	m.mu.Lock()
	id := m.nextL
	m.nextL++
	m.listeners[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

func (m *Manager) notify(now bool) {
	m.mu.Lock()
	if !now {
		if m.pending {
			m.mu.Unlock()
			return
		}
		m.pending = true
		m.mu.Unlock()
		time.AfterFunc(notifyEvery, func() {
			m.mu.Lock()
			m.pending = false
			m.mu.Unlock()
			m.notify(true)
		})
		return
	}
	fns := make([]func(), 0, len(m.listeners))
	for _, fn := range m.listeners {
		fns = append(fns, fn)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
