package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/cpamp"
	"github.com/licoy/cpa-bar/internal/store"
)

// MaxConcurrentRefresh bounds the instances refreshed at once.
const MaxConcurrentRefresh = 4

// SettingsStore loads and saves settings; store.File implements it.
type SettingsStore interface {
	Load() (store.Settings, error)
	Save(store.Settings) error
}

// ClientFactory builds the client of an instance. Tests replace it.
type ClientFactory func(p store.InstanceProfile, key string) (*cpa.Client, error)

// DefaultClientFactory builds real clients.
func DefaultClientFactory(p store.InstanceProfile, key string) (*cpa.Client, error) {
	return cpa.New(cpa.Options{
		InstanceID:            p.ID,
		InstanceName:          p.Name,
		BaseURL:               p.BaseURL,
		ManagementKey:         key,
		CACertPEM:             []byte(p.CACertPEM),
		ForwardAuthOnRedirect: p.ForwardAuthOnRedirect,
	})
}

// Options configures a Service.
type Options struct {
	Settings  SettingsStore
	Secrets   store.SecretStore
	NewClient ClientFactory
	// Now replaces the clock, for tests.
	Now func() time.Time
	// OnEvent receives deduplicated state transitions worth a
	// notification. It runs on a goroutine of the service.
	OnEvent func(Event)
}

// Service is the application state. Every method is safe from any
// goroutine; listeners run on the goroutine that changed the state.
type Service struct {
	opts Options

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}

	mu        sync.Mutex
	settings  store.Settings
	inst      map[string]*instance
	active    string // instance the user is looking at
	suspended bool
	listeners map[int]func()
	nextL     int
	started   bool
	wg        sync.WaitGroup
}

type instance struct {
	profile store.InstanceProfile
	client  *cpa.Client
	keyErr  error // why there is no client
	// plus reads the usage from CPA-Manager-Plus; plusErr is why there is
	// none for an instance with it.
	plus    *cpamp.Client
	plusErr error
	snap    InstanceSnapshot
	// gen changes whenever the connection changes or the instance is
	// removed; responses of an older generation are dropped.
	gen        uint64
	cancel     context.CancelFunc
	refreshing bool
	// authBlocked stops automatic requests after a 401/403.
	authBlocked bool
	nextDue     time.Time
	// notified holds the abnormal credentials already announced.
	notified map[string]bool
	wasOK    bool
}

// New loads the settings and keys and builds the service. It does not
// start refreshing: call Start.
func New(o Options) (*Service, error) {
	if o.NewClient == nil {
		o.NewClient = DefaultClientFactory
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	settings, err := o.Settings.Load()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		opts:      o,
		ctx:       ctx,
		cancel:    cancel,
		sem:       make(chan struct{}, MaxConcurrentRefresh),
		settings:  settings,
		inst:      map[string]*instance{},
		listeners: map[int]func(){},
	}
	for _, p := range settings.Instances {
		s.inst[p.ID] = s.newInstance(p)
	}
	return s, nil
}

func (s *Service) newInstance(p store.InstanceProfile) *instance {
	in := &instance{profile: p, notified: map[string]bool{}}
	in.snap.Profile = p
	s.connect(in)
	if p.Paused {
		in.snap.Conn = ConnPaused
	}
	return in
}

// connect (re)builds the client from the stored key. Caller holds s.mu or
// owns in exclusively.
func (s *Service) connect(in *instance) {
	in.client, in.keyErr = nil, nil
	in.plus, in.plusErr = nil, nil
	if in.profile.ManagerPlus() {
		key, err := s.opts.Secrets.Get(store.ManagerKeyID(in.profile.ID))
		if err == nil {
			in.plus, err = plusClient(in.profile, key)
		}
		in.plusErr = err
	}
	key, err := s.opts.Secrets.Get(in.profile.ID)
	if err != nil {
		in.keyErr = err
		return
	}
	c, err := s.opts.NewClient(in.profile, key)
	if err != nil {
		in.keyErr = err
		return
	}
	in.client = c
}

// Start begins background refreshing: every instance at once, then on
// schedule.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	s.RefreshAll()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
				s.Tick()
			}
		}
	}()
}

// Close stops refreshing and cancels requests in flight.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Subscribe registers fn to run after every state change. It returns a
// function that removes it.
func (s *Service) Subscribe(fn func()) func() {
	s.mu.Lock()
	id := s.nextL
	s.nextL++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *Service) notify() {
	s.mu.Lock()
	fns := make([]func(), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// ---- Reading state ----

// Settings returns a copy of the settings.
func (s *Service) Settings() store.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.Clone()
}

// Snapshots returns every instance in the user's order.
func (s *Service) Snapshots() []InstanceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceSnapshot, 0, len(s.settings.Instances))
	for _, p := range s.settings.Instances {
		if in := s.inst[p.ID]; in != nil {
			out = append(out, in.snap)
		}
	}
	return out
}

// Snapshot returns one instance.
func (s *Service) Snapshot(id string) (InstanceSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.inst[id]
	if in == nil {
		return InstanceSnapshot{}, false
	}
	return in.snap, true
}

// Overview sums every monitored instance.
func (s *Service) Overview() Overview { return overviewOf(s.Snapshots()) }

// Client returns the client of an instance for operations of the
// management window, or why there is none.
func (s *Service) Client(id string) (*cpa.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.inst[id]
	if in == nil {
		return nil, ErrNoInstance
	}
	if in.client == nil {
		return nil, in.keyErr
	}
	return in.client, nil
}

// ManagerPlus returns the CPA-Manager-Plus client of an instance; nil
// without an error when the instance has none.
func (s *Service) ManagerPlus(id string) (*cpamp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.inst[id]
	if in == nil {
		return nil, ErrNoInstance
	}
	return in.plus, in.plusErr
}

// ErrNoInstance means the instance was removed.
var ErrNoInstance = errors.New("instance not found")

// ---- Scheduling ----

// SetActive marks the instance the user is looking at, refreshed more
// often; "" for none.
func (s *Service) SetActive(id string) {
	s.mu.Lock()
	changed := s.active != id
	s.active = id
	if in := s.inst[id]; changed && in != nil {
		in.nextDue = time.Time{} // refresh soon after switching
	}
	s.mu.Unlock()
	if changed {
		s.Tick()
	}
}

// Suspend pauses refreshing, as the computer sleeps, and cancels requests
// in flight.
func (s *Service) Suspend() {
	s.mu.Lock()
	s.suspended = true
	for _, in := range s.inst {
		if in.cancel != nil {
			in.cancel()
		}
	}
	s.mu.Unlock()
}

// Resume refreshes every instance once and continues on schedule.
func (s *Service) Resume() {
	s.mu.Lock()
	s.suspended = false
	s.mu.Unlock()
	s.RefreshAll()
}

// Tick refreshes the instances whose time has come. The scheduler calls it
// every second; tests call it with a fake clock.
func (s *Service) Tick() {
	now := s.opts.Now()
	var due []string
	s.mu.Lock()
	if s.suspended {
		s.mu.Unlock()
		return
	}
	for _, p := range s.settings.Instances {
		in := s.inst[p.ID]
		if in == nil || in.profile.Paused || in.authBlocked || in.refreshing {
			continue
		}
		if !in.nextDue.After(now) {
			due = append(due, p.ID)
		}
	}
	s.mu.Unlock()
	for _, id := range due {
		s.refresh(id, false)
	}
}

// RefreshAll refreshes every monitored instance now.
func (s *Service) RefreshAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.settings.Instances))
	for _, p := range s.settings.Instances {
		if !p.Paused {
			ids = append(ids, p.ID)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.refresh(id, false)
	}
}

// Refresh refreshes one instance now, as the user asked: it also tries an
// instance blocked by an authentication error once.
func (s *Service) Refresh(id string) { s.refresh(id, true) }

func (s *Service) interval(id string) time.Duration {
	if id == s.active {
		return time.Duration(s.settings.ActiveRefresh) * time.Second
	}
	return time.Duration(s.settings.BackgroundRefresh) * time.Second
}

func (s *Service) refresh(id string, manual bool) {
	s.mu.Lock()
	in := s.inst[id]
	if in == nil || in.refreshing || s.suspended || in.profile.Paused || (in.authBlocked && !manual) {
		s.mu.Unlock()
		return
	}
	now := s.opts.Now()
	in.snap.LastAttempt = now
	in.nextDue = now.Add(s.interval(id))
	if in.client == nil {
		s.applyFailure(in, in.keyErr, ConnAuth)
		s.mu.Unlock()
		s.notify()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	in.refreshing = true
	in.cancel = cancel
	in.snap.Refreshing = true
	gen := in.gen
	client := in.client
	s.wg.Add(1)
	s.mu.Unlock()
	s.notify()

	go func() {
		defer s.wg.Done()
		defer cancel()
		var list *cpa.CredentialList
		var err error
		var took time.Duration
		select {
		case s.sem <- struct{}{}:
			start := time.Now()
			list, err = client.ListCredentials(ctx)
			took = time.Since(start)
			<-s.sem
		case <-ctx.Done():
			err = ctx.Err()
		}
		s.finish(id, in, gen, client, list, err, took)
	}()
}

func (s *Service) finish(id string, in *instance, gen uint64, client *cpa.Client, list *cpa.CredentialList, err error, took time.Duration) {
	s.mu.Lock()
	if s.inst[id] != in || in.gen != gen {
		// Removed or reconnected meanwhile: the response belongs to an
		// old connection.
		s.mu.Unlock()
		return
	}
	in.refreshing = false
	in.cancel = nil
	in.snap.Refreshing = false
	in.snap.Server = client.ServerInfo()
	var events []Event
	switch {
	case err == nil:
		events = s.applySuccess(in, list)
		in.snap.History = appendSample(in.snap.History, Sample{At: s.opts.Now(), OK: true, Latency: took, Accounts: in.snap.Accounts})
	case errors.Is(err, context.Canceled) || cpa.KindOf(err) == cpa.KindCanceled:
		// Canceled by sleep or removal: keep the previous state.
		in.nextDue = time.Time{}
	default:
		events = s.applyFailure(in, err, connStateFor(err))
		in.snap.History = appendSample(in.snap.History, Sample{At: s.opts.Now(), Latency: took})
	}
	s.mu.Unlock()
	s.notify()
	s.emit(events)
}

func connStateFor(err error) ConnState {
	switch cpa.KindOf(err) {
	case cpa.KindNetwork, cpa.KindTimeout:
		return ConnOffline
	case cpa.KindUnauthorized, cpa.KindForbidden:
		return ConnAuth
	case cpa.KindManagementUnavailable:
		return ConnUnavailable
	}
	return ConnError
}

func (s *Service) applySuccess(in *instance, list *cpa.CredentialList) []Event {
	now := s.opts.Now()
	in.authBlocked = false
	sn := &in.snap
	if in.profile.Paused {
		sn.Conn = ConnPaused
	} else {
		sn.Conn = ConnOnline
	}
	sn.Credentials = list.Files
	sn.Accounts = Summarize(list.Files)
	sn.HasData = true
	sn.Stale = false
	sn.Err = nil
	sn.LastSuccess = now

	var events []Event
	if in.notifiedOffline() {
		events = append(events, Event{Kind: EventRecovered, InstanceID: in.profile.ID, InstanceName: in.profile.Name})
	}
	in.wasOK = true
	delete(in.notified, offlineKey)

	// Announce credentials that became abnormal; forget recovered ones so
	// a relapse is announced again.
	abnormal := map[string]cpa.Credential{}
	for _, c := range list.Files {
		if h := c.Health(); h == cpa.HealthError || h == cpa.HealthCooling {
			abnormal[credKey(c)] = c
		}
	}
	var fresh []string
	for k, c := range abnormal {
		if !in.notified[k] {
			in.notified[k] = true
			fresh = append(fresh, c.DisplayName())
		}
	}
	for k := range in.notified {
		if _, ok := abnormal[k]; !ok && k != offlineKey {
			delete(in.notified, k)
		}
	}
	if len(fresh) > 0 {
		slices.Sort(fresh)
		events = append(events, Event{Kind: EventAccountsAbnormal, InstanceID: in.profile.ID,
			InstanceName: in.profile.Name, Accounts: fresh})
	}
	return events
}

const offlineKey = "\x00offline"

func (in *instance) notifiedOffline() bool { return in.notified[offlineKey] }

func credKey(c cpa.Credential) string {
	if c.AuthIndex != "" {
		return c.AuthIndex
	}
	return c.ID + "\x00" + c.Name
}

func (s *Service) applyFailure(in *instance, err error, state ConnState) []Event {
	sn := &in.snap
	sn.Err = err
	if in.profile.Paused {
		sn.Conn = ConnPaused
	} else {
		sn.Conn = state
	}
	sn.Stale = sn.HasData
	if state == ConnAuth {
		in.authBlocked = true
	}
	var events []Event
	if in.wasOK && !in.notified[offlineKey] {
		in.notified[offlineKey] = true
		events = append(events, Event{Kind: EventInstanceDown, InstanceID: in.profile.ID,
			InstanceName: in.profile.Name, Conn: state, Err: err})
	}
	in.wasOK = false
	return events
}

func (s *Service) emit(events []Event) {
	if s.opts.OnEvent == nil {
		return
	}
	for _, e := range events {
		s.opts.OnEvent(e)
	}
}

// ---- Instances ----

// NewInstanceID returns a random stable ID.
func NewInstanceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// InstanceInput is what the user enters for an instance.
type InstanceInput struct {
	Name    string
	Address string
	// Key is the management key; empty keeps the stored key on update.
	Key       string
	CACertPEM string
	// ForwardAuthOnRedirect allows credentials to follow redirects to a
	// different host for deployments that require it.
	ForwardAuthOnRedirect bool
	// Icon and Color are the instance's mark, empty for the default.
	Icon  string
	Color string
	// Flavor is store.FlavorManagerPlus for CPA with CPA-Manager-Plus,
	// whose address is ManagerURL and admin key ManagerKey; an empty
	// ManagerKey keeps the stored one on update.
	Flavor     string
	ManagerURL string
	ManagerKey string
}

func (in InstanceInput) profile(id string) (store.InstanceProfile, error) {
	base, err := cpa.NormalizeManagementURL(in.Address)
	if err != nil {
		return store.InstanceProfile{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimPrefix(strings.TrimPrefix(cpa.RootURL(base), "http://"), "https://")
	}
	p := store.InstanceProfile{
		ID: id, Name: name, BaseURL: base, CACertPEM: strings.TrimSpace(in.CACertPEM),
		ForwardAuthOnRedirect: in.ForwardAuthOnRedirect, Icon: in.Icon, Color: in.Color,
	}
	if in.Flavor == store.FlavorManagerPlus {
		mu, err := cpamp.NormalizeURL(in.ManagerURL)
		if err != nil {
			return store.InstanceProfile{}, fmt.Errorf("CPA-Manager-Plus: %w", err)
		}
		p.Flavor, p.ManagerURL = store.FlavorManagerPlus, mu
	}
	return p, nil
}

// plusClient is the CPA-Manager-Plus client of a profile, nil for CPA
// alone.
func plusClient(p store.InstanceProfile, key string) (*cpamp.Client, error) {
	if !p.ManagerPlus() {
		return nil, nil
	}
	return cpamp.New(cpamp.Options{
		BaseURL: p.ManagerURL, AdminKey: key, CACertPEM: []byte(p.CACertPEM),
		ForwardAuthOnRedirect: p.ForwardAuthOnRedirect,
	})
}

// managerKey is the admin key an input gives, or the stored one.
func (s *Service) managerKey(input InstanceInput, existingID string) (string, error) {
	if k := strings.TrimSpace(input.ManagerKey); k != "" || existingID == "" {
		if k == "" {
			return "", errors.New("the CPA-Manager-Plus admin key is required")
		}
		return k, nil
	}
	k, err := s.opts.Secrets.Get(store.ManagerKeyID(existingID))
	if errors.Is(err, store.ErrSecretNotFound) {
		return "", errors.New("the CPA-Manager-Plus admin key is required")
	}
	return k, err
}

// TestConnection checks an address and key without saving anything: it
// reads the configuration and the credentials, as adding does.
func (s *Service) TestConnection(ctx context.Context, input InstanceInput, existingID string) error {
	p, err := input.profile(existingID)
	if err != nil {
		return err
	}
	key := input.Key
	if key == "" && existingID != "" {
		if key, err = s.opts.Secrets.Get(existingID); err != nil {
			return err
		}
	}
	if key == "" {
		return errors.New("management key is required")
	}
	c, err := s.opts.NewClient(p, key)
	if err != nil {
		return err
	}
	if err := c.Probe(ctx); err != nil {
		return err
	}
	if !p.ManagerPlus() {
		return nil
	}
	mk, err := s.managerKey(input, existingID)
	if err != nil {
		return err
	}
	pc, err := plusClient(p, mk)
	if err != nil {
		return err
	}
	if err := pc.Probe(ctx); err != nil {
		return fmt.Errorf("CPA-Manager-Plus: %w", err)
	}
	return nil
}

// Detection is what an address turned out to be.
type Detection struct {
	// ManagerPlus is whether the address is CPA-Manager-Plus itself.
	ManagerPlus bool
	// ManagerURL is CPA-Manager-Plus on the same host as the CPA at the
	// address, found at its default port; empty when there is none.
	ManagerURL string
}

// Detect tells CPA-Manager-Plus from CPA at an address, and looks for
// CPA-Manager-Plus beside a CPA at its default port. It only reads
// /health, which needs no key.
func (s *Service) Detect(ctx context.Context, address, caPEM string) (Detection, error) {
	root, err := cpamp.NormalizeURL(address)
	if err != nil {
		return Detection{}, err
	}
	probe := func(addr string) (bool, error) {
		c, err := cpamp.New(cpamp.Options{BaseURL: addr, CACertPEM: []byte(caPEM)})
		if err != nil {
			return false, err
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = c.Detect(ctx)
		if errors.Is(err, cpamp.ErrNotManagerPlus) {
			return false, nil
		}
		return err == nil, err
	}
	found, err := probe(root)
	if found {
		return Detection{ManagerPlus: true}, nil
	}
	if err != nil {
		return Detection{}, err
	}
	u, err := url.Parse(root)
	if err != nil || u.Port() == cpamp.DefaultPort {
		return Detection{}, nil
	}
	beside := "http://" + net.JoinHostPort(u.Hostname(), cpamp.DefaultPort)
	if ok, _ := probe(beside); ok {
		return Detection{ManagerURL: beside}, nil
	}
	return Detection{}, nil
}

// ManagerCPAAddress is the CPA address CPA-Manager-Plus collects from, to
// fill in the CPA when the user gave the address of Manager-Plus.
func (s *Service) ManagerCPAAddress(ctx context.Context, opts cpamp.Options) (string, error) {
	c, err := cpamp.New(opts)
	if err != nil {
		return "", err
	}
	return c.CPAAddress(ctx)
}

type secretBackup struct {
	id     string
	key    string
	exists bool
}

type secretChange struct {
	backup secretBackup
	set    bool
	key    string
}

func readSecretBackup(secrets store.SecretStore, id string) (secretBackup, error) {
	key, err := secrets.Get(id)
	if err == nil {
		return secretBackup{id: id, key: key, exists: true}, nil
	}
	if errors.Is(err, store.ErrSecretNotFound) {
		return secretBackup{id: id}, nil
	}
	return secretBackup{}, fmt.Errorf("read credential %q: %w", id, err)
}

func restoreSecrets(secrets store.SecretStore, backups []secretBackup) error {
	var result error
	for i := len(backups) - 1; i >= 0; i-- {
		b := backups[i]
		var err error
		if b.exists {
			err = secrets.Set(b.id, b.key)
		} else {
			err = secrets.Delete(b.id)
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("restore credential %q: %w", b.id, err))
		}
	}
	return result
}

func secretChangeError(secrets store.SecretStore, id, action string, err error, attempted []secretBackup) error {
	return errors.Join(fmt.Errorf("%s credential %q: %w", action, id, err), restoreSecrets(secrets, attempted))
}

// AddInstance confirms the management API answers, stores the key, then
// saves the instance. Failures restore the previous credential state when
// possible and include any cleanup error in the result.
func (s *Service) AddInstance(ctx context.Context, input InstanceInput) (string, error) {
	if strings.TrimSpace(input.Key) == "" {
		return "", errors.New("management key is required")
	}
	id := NewInstanceID()
	p, err := input.profile(id)
	if err != nil {
		return "", err
	}
	if err := s.TestConnection(ctx, input, ""); err != nil {
		return "", err
	}
	mainBackup := secretBackup{id: id}
	attempted := []secretBackup{mainBackup}
	if err := s.opts.Secrets.Set(id, input.Key); err != nil {
		return "", secretChangeError(s.opts.Secrets, id, "store", err, attempted)
	}
	if p.ManagerPlus() {
		managerID := store.ManagerKeyID(id)
		attempted = append(attempted, secretBackup{id: managerID})
		if err := s.opts.Secrets.Set(managerID, strings.TrimSpace(input.ManagerKey)); err != nil {
			return "", secretChangeError(s.opts.Secrets, managerID, "store", err, attempted)
		}
	}
	p.CreatedAt = s.opts.Now()
	s.mu.Lock()
	p.Order = len(s.settings.Instances)
	next := s.settings.Clone()
	next.Instances = append(next.Instances, p)
	if err := s.saveLocked(next); err != nil {
		s.mu.Unlock()
		return "", errors.Join(err, restoreSecrets(s.opts.Secrets, attempted))
	}
	s.inst[id] = s.newInstance(p)
	s.mu.Unlock()
	s.notify()
	s.refresh(id, true)
	return id, nil
}

// UpdateInstance changes an instance. A new address or key reconnects it,
// dropping responses of the old connection.
func (s *Service) UpdateInstance(id string, input InstanceInput) error {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	in := s.inst[id]
	if in == nil {
		return ErrNoInstance
	}
	p, err := input.profile(id)
	if err != nil {
		return err
	}
	old := in.profile
	p.Order, p.Paused, p.CreatedAt = old.Order, old.Paused, old.CreatedAt
	p.PinnedAccounts = old.PinnedAccounts

	if p.ManagerPlus() && strings.TrimSpace(input.ManagerKey) == "" {
		if _, err := s.managerKey(input, id); err != nil {
			return err
		}
	}
	var changes []secretChange
	if input.Key != "" {
		backup, err := readSecretBackup(s.opts.Secrets, id)
		if err != nil {
			return err
		}
		changes = append(changes, secretChange{backup: backup, set: true, key: input.Key})
	}
	plusKey := strings.TrimSpace(input.ManagerKey)
	if p.ManagerPlus() && plusKey != "" {
		backup, err := readSecretBackup(s.opts.Secrets, store.ManagerKeyID(id))
		if err != nil {
			return err
		}
		changes = append(changes, secretChange{backup: backup, set: true, key: plusKey})
	} else if !p.ManagerPlus() && old.ManagerPlus() {
		backup, err := readSecretBackup(s.opts.Secrets, store.ManagerKeyID(id))
		if err != nil {
			return err
		}
		changes = append(changes, secretChange{backup: backup})
	}
	var attempted []secretBackup
	for _, change := range changes {
		attempted = append(attempted, change.backup)
		action := "delete"
		if change.set {
			action = "store"
			err = s.opts.Secrets.Set(change.backup.id, change.key)
		} else {
			err = s.opts.Secrets.Delete(change.backup.id)
		}
		if err != nil {
			return secretChangeError(s.opts.Secrets, change.backup.id, action, err, attempted)
		}
	}

	next := s.settings.Clone()
	if q := next.Instance(id); q != nil {
		*q = p
	}
	if err := s.saveLocked(next); err != nil {
		return errors.Join(err, restoreSecrets(s.opts.Secrets, attempted))
	}
	reconnect := input.Key != "" || p.BaseURL != old.BaseURL || p.CACertPEM != old.CACertPEM ||
		p.ForwardAuthOnRedirect != old.ForwardAuthOnRedirect
	in.profile = p
	in.snap.Profile = p
	if !reconnect && (p.ManagerPlus() != old.ManagerPlus() || p.ManagerURL != old.ManagerURL || plusKey != "") {
		s.connect(in) // only the usage source changed
	}
	if reconnect {
		if in.cancel != nil {
			in.cancel()
		}
		in.gen++
		in.refreshing = false
		in.snap.Refreshing = false
		in.authBlocked = false
		if p.BaseURL != old.BaseURL {
			// Another server: its data is not this one's.
			in.snap = InstanceSnapshot{Profile: p}
			in.notified = map[string]bool{}
			in.wasOK = false
		}
		s.connect(in)
	} else if in.client != nil && p.Name != old.Name {
		s.connect(in) // errors name the instance
	}
	locked = false
	s.mu.Unlock()
	s.notify()
	if reconnect {
		s.refresh(id, true)
	}
	return nil
}

// RemoveInstance cancels its requests and deletes only the local record
// and its stored key. Credentials on the server are untouched.
func (s *Service) RemoveInstance(id string) error {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	in := s.inst[id]
	if in == nil {
		return ErrNoInstance
	}
	mainBackup, err := readSecretBackup(s.opts.Secrets, id)
	if err != nil {
		return err
	}
	backups := []secretBackup{mainBackup}
	if in.profile.ManagerPlus() {
		managerBackup, err := readSecretBackup(s.opts.Secrets, store.ManagerKeyID(id))
		if err != nil {
			return err
		}
		backups = append(backups, managerBackup)
	}
	var attempted []secretBackup
	for _, backup := range backups {
		attempted = append(attempted, backup)
		if err := s.opts.Secrets.Delete(backup.id); err != nil {
			return secretChangeError(s.opts.Secrets, backup.id, "delete", err, attempted)
		}
	}
	next := s.settings.Clone()
	next.Instances = slices.DeleteFunc(next.Instances, func(p store.InstanceProfile) bool { return p.ID == id })
	if next.DefaultInstanceID == id {
		next.DefaultInstanceID = ""
	}
	if err := s.saveLocked(next); err != nil {
		return errors.Join(err, restoreSecrets(s.opts.Secrets, attempted))
	}
	if in.cancel != nil {
		in.cancel()
	}
	in.gen++
	delete(s.inst, id)
	if s.active == id {
		s.active = ""
	}
	locked = false
	s.mu.Unlock()
	s.notify()
	return nil
}

// MoveInstance moves an instance by delta places in the order.
func (s *Service) MoveInstance(id string, delta int) error {
	s.mu.Lock()
	next := s.settings.Clone()
	i := slices.IndexFunc(next.Instances, func(p store.InstanceProfile) bool { return p.ID == id })
	if i < 0 {
		s.mu.Unlock()
		return ErrNoInstance
	}
	j := max(0, min(len(next.Instances)-1, i+delta))
	if i == j {
		s.mu.Unlock()
		return nil
	}
	p := next.Instances[i]
	next.Instances = slices.Delete(next.Instances, i, i+1)
	next.Instances = slices.Insert(next.Instances, j, p)
	for k := range next.Instances {
		next.Instances[k].Order = k
	}
	err := s.saveLocked(next)
	if err == nil {
		for _, q := range next.Instances {
			if in := s.inst[q.ID]; in != nil {
				in.profile.Order = q.Order
				in.snap.Profile.Order = q.Order
			}
		}
	}
	s.mu.Unlock()
	s.notify()
	return err
}

// SetPaused pauses or resumes monitoring of an instance.
func (s *Service) SetPaused(id string, paused bool) error {
	s.mu.Lock()
	in := s.inst[id]
	if in == nil {
		s.mu.Unlock()
		return ErrNoInstance
	}
	next := s.settings.Clone()
	next.Instance(id).Paused = paused
	if err := s.saveLocked(next); err != nil {
		s.mu.Unlock()
		return err
	}
	in.profile.Paused = paused
	in.snap.Profile.Paused = paused
	if paused {
		if in.cancel != nil {
			in.cancel()
		}
		in.gen++
		in.refreshing = false
		in.snap.Refreshing = false
		in.snap.Conn = ConnPaused
	} else {
		in.snap.Conn = ConnUnknown
		in.nextDue = time.Time{}
	}
	s.mu.Unlock()
	s.notify()
	if !paused {
		s.refresh(id, true)
	}
	return nil
}

// SetCredentialPinned pins the account named name (its file name on the
// server) to the top of the instance's lists, after those pinned before
// it, or unpins it.
func (s *Service) SetCredentialPinned(id, name string, pinned bool) error {
	s.mu.Lock()
	in := s.inst[id]
	if in == nil {
		s.mu.Unlock()
		return ErrNoInstance
	}
	next := s.settings.Clone()
	p := next.Instance(id)
	if name == "" || p.Pinned(name) == pinned {
		s.mu.Unlock()
		return nil
	}
	p.PinnedAccounts = slices.DeleteFunc(p.PinnedAccounts, func(n string) bool { return n == name })
	if pinned {
		p.PinnedAccounts = append(p.PinnedAccounts, name)
	}
	if err := s.saveLocked(next); err != nil {
		s.mu.Unlock()
		return err
	}
	in.profile.PinnedAccounts = p.PinnedAccounts
	in.snap.Profile.PinnedAccounts = p.PinnedAccounts
	s.mu.Unlock()
	s.notify()
	return nil
}

// SetDefaultInstance sets the instance the panel opens on; "" for the
// overview of all.
func (s *Service) SetDefaultInstance(id string) error {
	return s.UpdateSettings(func(st *store.Settings) { st.DefaultInstanceID = id })
}

// UpdateSettings changes the settings and saves them.
func (s *Service) UpdateSettings(fn func(*store.Settings)) error {
	s.mu.Lock()
	next := s.settings.Clone()
	fn(&next)
	next.Normalize()
	err := s.saveLocked(next)
	s.mu.Unlock()
	s.notify()
	return err
}

// saveLocked persists next and makes it current only once it is saved.
func (s *Service) saveLocked(next store.Settings) error {
	if err := s.opts.Settings.Save(next); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	s.settings = next
	return nil
}

// ---- Operations shared by the panel and the window ----

// SetCredentialDisabled enables or disables a credential, then reads the
// server's state again.
func (s *Service) SetCredentialDisabled(ctx context.Context, id string, c cpa.Credential, disabled bool) error {
	client, err := s.Client(id)
	if err != nil {
		return err
	}
	name := c.Name
	if name == "" {
		name = c.ID
	}
	err = client.SetCredentialDisabled(ctx, name, c.AuthIndex, disabled)
	// Even an indeterminate result is worth a fresh read: it shows what
	// happened, without sending the write again.
	s.Refresh(id)
	return err
}
