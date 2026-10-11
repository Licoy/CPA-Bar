package core

// EventKind is a state transition worth telling the user about.
type EventKind int

const (
	// EventInstanceDown: an instance that was online failed. Sent once
	// until it recovers.
	EventInstanceDown EventKind = iota
	// EventRecovered: an instance announced down is online again.
	EventRecovered
	// EventAccountsAbnormal: credentials newly in error or cooldown.
	// Each is announced once until it recovers.
	EventAccountsAbnormal
)

// Event is a deduplicated notification.
type Event struct {
	Kind         EventKind
	InstanceID   string
	InstanceName string
	Conn         ConnState
	Err          error
	// Accounts names the credentials of EventAccountsAbnormal.
	Accounts []string
}
