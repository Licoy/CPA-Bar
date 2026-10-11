// Package core is CPA-Bar's application state: the instances, their health
// snapshots, the refresh scheduler and the operations the UI performs. It
// knows nothing of windows; the UI subscribes to changes and reads
// snapshots.
package core

import (
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

// ConnState is an instance's connection state.
type ConnState int

const (
	// ConnUnknown: not checked yet.
	ConnUnknown ConnState = iota
	ConnOnline
	// ConnOffline: unreachable or timed out.
	ConnOffline
	// ConnAuth: the key is missing or rejected (401/403). Automatic
	// requests stop until the user fixes the key or refreshes by hand.
	ConnAuth
	// ConnUnavailable: the v8 management API answered 404: an older CPA,
	// management disabled, or a wrong address. Not "offline".
	ConnUnavailable
	// ConnError: another failure (5xx, invalid response, rate limited…).
	ConnError
	// ConnPaused: the user paused monitoring.
	ConnPaused
)

// AccountSummary counts credentials by CPA's reported run state.
type AccountSummary struct {
	Total     int
	Available int
	Cooling   int
	Error     int
	Disabled  int
	Pending   int
}

// Abnormal is the number of credentials that need attention: errors and
// cooldowns. Disabled ones are the user's choice and do not count.
func (a AccountSummary) Abnormal() int { return a.Error + a.Cooling }

func (a *AccountSummary) add(b AccountSummary) {
	a.Total += b.Total
	a.Available += b.Available
	a.Cooling += b.Cooling
	a.Error += b.Error
	a.Disabled += b.Disabled
	a.Pending += b.Pending
}

// Summarize counts credentials.
func Summarize(creds []cpa.Credential) AccountSummary {
	var s AccountSummary
	for _, c := range creds {
		s.Total++
		switch c.Health() {
		case cpa.HealthAvailable:
			s.Available++
		case cpa.HealthCooling:
			s.Cooling++
		case cpa.HealthError:
			s.Error++
		case cpa.HealthDisabled:
			s.Disabled++
		case cpa.HealthPending:
			s.Pending++
		}
	}
	return s
}

// InstanceSnapshot is an immutable view of one instance for the UI.
type InstanceSnapshot struct {
	Profile store.InstanceProfile
	Conn    ConnState
	// Accounts and Credentials are from the last successful refresh; when
	// Stale, they are older than the current error and must be shown as
	// such, never as zero or healthy.
	Accounts    AccountSummary
	Credentials []cpa.Credential
	HasData     bool
	Stale       bool
	LastSuccess time.Time
	LastAttempt time.Time
	Err         error
	Refreshing  bool
	Server      cpa.ServerInfo
	// History holds the latest refreshes, oldest first, for charts. It is
	// replaced, never changed in place, so snapshots may share it.
	History []Sample
}

// Sample is the outcome of one refresh.
type Sample struct {
	At time.Time
	OK bool
	// Latency is how long the server took to answer.
	Latency  time.Duration
	Accounts AccountSummary
}

// HistoryLen is how many refreshes a snapshot keeps.
const HistoryLen = 60

func appendSample(h []Sample, smp Sample) []Sample {
	start := max(0, len(h)+1-HistoryLen)
	out := make([]Sample, 0, len(h)-start+1)
	return append(append(out, h[start:]...), smp)
}

// Overview sums the instances that are monitored.
type Overview struct {
	Instances int
	Online    int
	// Problem counts instances that are neither online nor paused.
	Problem  int
	Accounts AccountSummary
	// Stale reports that some account counts come from stale data.
	Stale bool
}

// Healthy reports whether nothing needs attention.
func (o Overview) Healthy() bool {
	return o.Problem == 0 && o.Accounts.Abnormal() == 0
}

func overviewOf(snaps []InstanceSnapshot) Overview {
	var o Overview
	for _, s := range snaps {
		if s.Conn == ConnPaused {
			continue
		}
		o.Instances++
		switch s.Conn {
		case ConnOnline:
			o.Online++
		case ConnUnknown:
		default:
			o.Problem++
		}
		if s.HasData {
			o.Accounts.add(s.Accounts)
			if s.Stale {
				o.Stale = true
			}
		}
	}
	return o
}
