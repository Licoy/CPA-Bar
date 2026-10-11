// Package store keeps CPA-Bar's local state: non-sensitive settings in a
// versioned JSON file and management keys in the system credential store.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is the current settings file version.
const SchemaVersion = 1

// FileName is the settings file in the app's user data directory.
const FileName = "settings.json"

// InstanceProfile is a CPA instance the user manages. Its management key is
// not here: it lives in the credential store under ID.
type InstanceProfile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"` // normalized v8 management URL
	Order   int    `json:"order"`
	// Paused stops background monitoring of the instance.
	Paused bool `json:"paused,omitempty"`
	// CACertPEM trusts a CA of the user's own for this instance.
	CACertPEM string `json:"caCertPem,omitempty"`
	// Icon and Color are the instance's mark: a glyph and a tint name the
	// view knows, empty for the default.
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
	// Flavor is FlavorManagerPlus when CPA-Manager-Plus collects the
	// instance's usage, at ManagerURL; its admin key is in the credential
	// store under ManagerKeyID(ID).
	Flavor     string    `json:"flavor,omitempty"`
	ManagerURL string    `json:"managerUrl,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Flavors of an instance.
const (
	// FlavorOfficial is CPA alone.
	FlavorOfficial = ""
	// FlavorManagerPlus is CPA with CPA-Manager-Plus collecting its usage.
	FlavorManagerPlus = "manager-plus"
)

// ManagerPlus reports whether the usage is read from CPA-Manager-Plus.
func (p InstanceProfile) ManagerPlus() bool {
	return p.Flavor == FlavorManagerPlus && p.ManagerURL != ""
}

// ManagerKeyID is the credential store entry of the CPA-Manager-Plus admin
// key of an instance.
func ManagerKeyID(instanceID string) string { return instanceID + "/manager-plus" }

// Appearance values.
const (
	AppearanceSystem = "system"
	AppearanceLight  = "light"
	AppearanceDark   = "dark"
)

// Language values.
const (
	LanguageZH = "zh-CN"
	LanguageEN = "en"
)

// Settings is everything CPA-Bar keeps on disk apart from secrets.
type Settings struct {
	Version   int               `json:"version"`
	Instances []InstanceProfile `json:"instances"`
	// DefaultInstanceID is the instance the panel opens on; empty shows the
	// overview of all instances.
	DefaultInstanceID string `json:"defaultInstanceId,omitempty"`

	Appearance string `json:"appearance"`
	Language   string `json:"language"`

	// Refresh intervals in seconds.
	BackgroundRefresh int `json:"backgroundRefresh"`
	ActiveRefresh     int `json:"activeRefresh"`

	LaunchAtLogin bool `json:"launchAtLogin"`
	Notifications bool `json:"notifications"`
	// TrayText shows "3/4" next to the icon where the platform can.
	TrayText bool `json:"trayText"`
	// TrayStyle is what the menu bar shows: the icon (and TrayText), or
	// the quota of some accounts as batteries or bars.
	TrayStyle string `json:"trayStyle,omitempty"`
	// TrayIconColor shows the tray icon in the logo's colors. By default
	// it is in one color, which follows the menu bar or taskbar.
	TrayIconColor bool `json:"trayIconColor,omitempty"`

	AutoCheckUpdates bool `json:"autoCheckUpdates"`
	// Privacy hides the middle of addresses, emails and account names.
	Privacy bool `json:"privacy,omitempty"`
	// UsageOff stops collecting usage records. Collection is on by
	// default: CPA-Bar subscribes to the usage stream of each instance.
	UsageOff bool `json:"usageOff,omitempty"`
}

// The styles of the menu bar item.
const (
	// TrayStyleIcon is the logo, with the text of TrayText.
	TrayStyleIcon = ""
	// TrayStyleBattery shows the quota of up to six accounts as upright
	// batteries.
	TrayStyleBattery = "battery"
	// TrayStyleBars shows the quota of up to two accounts as bars, the
	// short limit over the long one.
	TrayStyleBars = "bars"
)

// Defaults returns the settings of a first launch.
func Defaults() Settings {
	return Settings{
		Version:           SchemaVersion,
		Instances:         []InstanceProfile{},
		Appearance:        AppearanceSystem,
		Language:          LanguageZH,
		BackgroundRefresh: 30,
		ActiveRefresh:     10,
		Notifications:     true,
		TrayText:          true,
		AutoCheckUpdates:  true,
	}
}

// Normalize fills invalid or missing values with defaults and orders the
// instances.
func (s *Settings) Normalize() {
	d := Defaults()
	if s.Instances == nil {
		s.Instances = []InstanceProfile{}
	}
	if s.Appearance != AppearanceLight && s.Appearance != AppearanceDark {
		s.Appearance = d.Appearance
	}
	if s.Language != LanguageEN && s.Language != LanguageZH {
		s.Language = d.Language
	}
	if s.BackgroundRefresh < 10 {
		s.BackgroundRefresh = d.BackgroundRefresh
	}
	if s.ActiveRefresh < 3 {
		s.ActiveRefresh = d.ActiveRefresh
	}
	slices.SortStableFunc(s.Instances, func(a, b InstanceProfile) int { return a.Order - b.Order })
	for i := range s.Instances {
		s.Instances[i].Order = i
	}
	if s.TrayStyle != TrayStyleBattery && s.TrayStyle != TrayStyleBars {
		s.TrayStyle = TrayStyleIcon
	}
	if s.DefaultInstanceID != "" && s.Instance(s.DefaultInstanceID) == nil {
		s.DefaultInstanceID = ""
	}
}

// Instance returns the profile with id, or nil.
func (s *Settings) Instance(id string) *InstanceProfile {
	for i := range s.Instances {
		if s.Instances[i].ID == id {
			return &s.Instances[i]
		}
	}
	return nil
}

// Clone returns a deep copy.
func (s Settings) Clone() Settings {
	s.Instances = slices.Clone(s.Instances)
	return s
}

// File reads and writes the settings file.
type File struct {
	Path string
}

// Load reads the settings. A missing file gives the defaults. A file of an
// older version is backed up next to itself before it is migrated; one of a
// newer version is refused rather than overwritten.
func (f File) Load() (Settings, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return Settings{}, fmt.Errorf("settings file %s is not valid JSON: %w", f.Path, err)
	}
	if head.Version > SchemaVersion {
		return Settings{}, fmt.Errorf("settings file %s is from a newer CPA-Bar (version %d)", f.Path, head.Version)
	}
	s := Defaults()
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, fmt.Errorf("settings file %s: %w", f.Path, err)
	}
	if head.Version < SchemaVersion {
		backup := fmt.Sprintf("%s.v%d.bak", f.Path, head.Version)
		if err := os.WriteFile(backup, b, 0o600); err != nil {
			return Settings{}, fmt.Errorf("back up settings before migration: %w", err)
		}
		migrate(&s, head.Version)
	}
	s.Version = SchemaVersion
	s.Normalize()
	return s, nil
}

// migrate upgrades settings read from an older schema in place.
func migrate(s *Settings, from int) {
	_ = from // version 0 (no version field) has the same fields as 1.
}

// Save writes the settings atomically: to a temporary file in the same
// directory, then renamed over the old one.
func (f File) Save(s Settings) error {
	s.Version = SchemaVersion
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(f.Path, b)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ---- Instance export ----

// ExportFormat identifies files of exported instances.
const ExportFormat = "cpa-bar.instances"

type exportFile struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	Instances []exportInstance `json:"instances"`
}

type exportInstance struct {
	Name       string `json:"name"`
	BaseURL    string `json:"baseUrl"`
	CACertPEM  string `json:"caCertPem,omitempty"`
	Icon       string `json:"icon,omitempty"`
	Color      string `json:"color,omitempty"`
	Flavor     string `json:"flavor,omitempty"`
	ManagerURL string `json:"managerUrl,omitempty"`
}

// ExportInstances serializes connection metadata only: never keys.
func ExportInstances(profiles []InstanceProfile) ([]byte, error) {
	out := exportFile{Format: ExportFormat, Version: 1, Instances: []exportInstance{}}
	for _, p := range profiles {
		out.Instances = append(out.Instances, exportInstance{Name: p.Name, BaseURL: p.BaseURL, CACertPEM: p.CACertPEM, Icon: p.Icon, Color: p.Color, Flavor: p.Flavor, ManagerURL: p.ManagerURL})
	}
	return json.MarshalIndent(out, "", "  ")
}

// ImportedInstance is a connection read from an export file, without an ID
// or a key: the caller assigns an ID and asks the user for the key.
type ImportedInstance struct {
	Name       string
	BaseURL    string
	CACertPEM  string
	Icon       string
	Color      string
	Flavor     string
	ManagerURL string
}

// ParseInstances reads an export file.
func ParseInstances(b []byte) ([]ImportedInstance, error) {
	var in exportFile
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("not a CPA-Bar instance file: %w", err)
	}
	if in.Format != ExportFormat {
		return nil, errors.New("not a CPA-Bar instance file")
	}
	out := make([]ImportedInstance, 0, len(in.Instances))
	for _, e := range in.Instances {
		if strings.TrimSpace(e.BaseURL) == "" {
			continue
		}
		out = append(out, ImportedInstance{Name: strings.TrimSpace(e.Name), BaseURL: strings.TrimSpace(e.BaseURL), CACertPEM: e.CACertPEM, Icon: e.Icon, Color: e.Color, Flavor: e.Flavor, ManagerURL: strings.TrimSpace(e.ManagerURL)})
	}
	return out, nil
}
