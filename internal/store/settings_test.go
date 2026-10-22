package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newFile(t *testing.T) File {
	t.Helper()
	return File{Path: filepath.Join(t.TempDir(), "data", FileName)}
}

func writeRaw(t *testing.T, f File, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	f := newFile(t)
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("Load() = %+v, want defaults %+v", got, Defaults())
	}
	if got.Version != SchemaVersion || got.Instances == nil {
		t.Errorf("defaults must carry the version and a non-nil instance list: %+v", got)
	}
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Error("Load of a missing file must not create it")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	f := newFile(t)
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	in := Defaults()
	in.Instances = []InstanceProfile{
		{ID: "a", Name: "Home", BaseURL: "http://127.0.0.1:8317/v8/management", Order: 0, CreatedAt: created},
		{ID: "b", Name: "Work", BaseURL: "https://h/cpa/v8/management", Order: 1, Paused: true, CACertPEM: "PEM", ForwardAuthOnRedirect: true, CreatedAt: created},
	}
	in.DefaultInstanceID = "b"
	in.Appearance = AppearanceDark
	in.Language = LanguageEN
	in.PanelOpaque = true
	in.PassiveQuota = true
	in.BackgroundRefresh = 60
	in.ActiveRefresh = 5
	in.LaunchAtLogin = true
	in.Notifications = false
	in.TrayText = false
	in.AutoCheckUpdates = false

	if err := f.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip changed settings:\n got %+v\nwant %+v", got, in)
	}
	// A saved file is not "migrated" on the next load.
	entries, _ := os.ReadDir(filepath.Dir(f.Path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			t.Errorf("unexpected backup %s after loading a current-version file", e.Name())
		}
	}
}

func TestSaveStampsCurrentVersionAndCreatesDirectory(t *testing.T) {
	f := newFile(t)
	s := Defaults()
	s.Version = 0
	if err := f.Save(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	var head struct{ Version int }
	if err := json.Unmarshal(b, &head); err != nil || head.Version != SchemaVersion {
		t.Errorf("saved version = %d, err = %v", head.Version, err)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	f := newFile(t)
	for i := 0; i < 5; i++ {
		s := Defaults()
		s.BackgroundRefresh = 30 + i
		if err := f.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(f.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only %s", names, FileName)
	}
}

func TestSaveFailureKeepsOldFileAndLeavesNoTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newFile(t)
	old := Defaults()
	old.BackgroundRefresh = 77
	if err := f.Save(old); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(f.Path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if err := f.Save(Defaults()); err == nil {
		t.Fatal("Save into a read-only directory succeeded")
	}
	os.Chmod(dir, 0o700)
	got, err := f.Load()
	if err != nil || got.BackgroundRefresh != 77 {
		t.Errorf("old file damaged: %+v, %v", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover files after failed save: %v", entries)
	}
}

func TestSavedFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on windows")
	}
	f := newFile(t)
	if err := f.Save(Defaults()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings file mode = %o, want 600", perm)
	}
	// Overwriting keeps the mode.
	if err := f.Save(Defaults()); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(f.Path)
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after second save = %o, want 600", perm)
	}
	dst, _ := os.Stat(filepath.Dir(f.Path))
	if perm := dst.Mode().Perm(); perm != 0o700 {
		t.Errorf("created directory mode = %o, want 700", perm)
	}
}

func TestLoadNewerVersionIsRefusedAndFileUntouched(t *testing.T) {
	f := newFile(t)
	content := `{"version": 99, "futureField": {"x": 1}, "instances": []}`
	writeRaw(t, f, content)

	_, err := f.Load()
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Load() error = %v, want a refusal mentioning a newer version", err)
	}
	b, _ := os.ReadFile(f.Path)
	if string(b) != content {
		t.Errorf("file was modified: %s", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(f.Path))
	if len(entries) != 1 {
		t.Errorf("Load of a newer file created extra files: %v", entries)
	}
}

func TestLoadVersionZeroIsBackedUpBeforeMigration(t *testing.T) {
	for name, content := range map[string]string{
		"no version field": `{"instances":[{"id":"a","name":"A","baseUrl":"http://h/v8/management","order":0,"createdAt":"2026-01-01T00:00:00Z"}],"language":"en","backgroundRefresh":45}`,
		"explicit zero":    `{"version":0,"instances":[],"language":"en"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFile(t)
			writeRaw(t, f, content)
			got, err := f.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != SchemaVersion {
				t.Errorf("Version = %d, want %d", got.Version, SchemaVersion)
			}
			if got.Language != LanguageEN {
				t.Errorf("settings of the old file lost: %+v", got)
			}
			backup, err := os.ReadFile(f.Path + ".v0.bak")
			if err != nil {
				t.Fatalf("no backup: %v", err)
			}
			if string(backup) != content {
				t.Errorf("backup differs from the original file:\n%s", backup)
			}
			if orig, _ := os.ReadFile(f.Path); string(orig) != content {
				t.Error("Load rewrote the original; only Save may do that")
			}
			if runtime.GOOS != "windows" {
				if st, _ := os.Stat(f.Path + ".v0.bak"); st.Mode().Perm() != 0o600 {
					t.Errorf("backup mode = %o", st.Mode().Perm())
				}
			}
		})
	}
	t.Run("fields missing from an old file take defaults", func(t *testing.T) {
		f := newFile(t)
		writeRaw(t, f, `{"instances":null}`)
		got, err := f.Load()
		if err != nil {
			t.Fatal(err)
		}
		want := Defaults()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestLoadWithoutPanelOpaqueKeepsTranslucentDefault(t *testing.T) {
	f := newFile(t)
	writeRaw(t, f, `{"version":1,"instances":[],"appearance":"system","language":"zh-CN","backgroundRefresh":30,"activeRefresh":10}`)
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PanelOpaque {
		t.Error("a settings file without panelOpaque became opaque")
	}
}

func TestLoadBackupFailureAbortsMigration(t *testing.T) {
	f := newFile(t)
	writeRaw(t, f, `{"instances":[]}`)
	// A directory squatting on the backup name makes the backup impossible.
	if err := os.Mkdir(f.Path+".v0.bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(); err == nil || !strings.Contains(err.Error(), "back up") {
		t.Errorf("Load() error = %v, want a backup failure", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":    "not json",
		"empty":      "",
		"truncated":  `{"version":1,"instances":[`,
		"wrong type": `{"version":1,"instances":"x"}`,
		"wrong ver":  `{"version":"one"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFile(t)
			writeRaw(t, f, content)
			if _, err := f.Load(); err == nil {
				t.Error("Load succeeded")
			}
			if b, _ := os.ReadFile(f.Path); string(b) != content {
				t.Error("an unreadable file must be left as it is")
			}
		})
	}
}

func TestLoadUnreadablePath(t *testing.T) {
	f := File{Path: t.TempDir()} // a directory, not a file
	if _, err := f.Load(); err == nil {
		t.Error("Load of a directory succeeded")
	}
}

func TestNormalize(t *testing.T) {
	t.Run("clamps intervals and fixes enums", func(t *testing.T) {
		d := Defaults()
		s := Settings{
			BackgroundRefresh: 9, ActiveRefresh: 2, Appearance: "neon", Language: "fr",
		}
		s.Normalize()
		if s.BackgroundRefresh != d.BackgroundRefresh || s.ActiveRefresh != d.ActiveRefresh {
			t.Errorf("intervals = %d/%d", s.BackgroundRefresh, s.ActiveRefresh)
		}
		if s.Appearance != AppearanceSystem || s.Language != LanguageAuto {
			t.Errorf("appearance/language = %q/%q", s.Appearance, s.Language)
		}
		if s.Instances == nil {
			t.Error("Instances must be non-nil")
		}
	})
	t.Run("keeps valid values at the boundary", func(t *testing.T) {
		s := Settings{BackgroundRefresh: 10, ActiveRefresh: 3, Appearance: AppearanceDark, Language: LanguageEN}
		s.Normalize()
		if s.BackgroundRefresh != 10 || s.ActiveRefresh != 3 || s.Appearance != AppearanceDark || s.Language != LanguageEN {
			t.Errorf("valid settings changed: %+v", s)
		}
		for _, ap := range []string{AppearanceLight, AppearanceDark, AppearanceSystem} {
			s := Settings{Appearance: ap}
			s.Normalize()
			if s.Appearance != ap {
				t.Errorf("appearance %q became %q", ap, s.Appearance)
			}
		}
		for _, language := range []string{LanguageAuto, LanguageZH, LanguageEN} {
			s := Settings{Language: language}
			s.Normalize()
			if s.Language != language {
				t.Errorf("language %q became %q", language, s.Language)
			}
		}
	})
	t.Run("negative and zero intervals use defaults", func(t *testing.T) {
		s := Settings{BackgroundRefresh: -5, ActiveRefresh: 0}
		s.Normalize()
		if s.BackgroundRefresh != 30 || s.ActiveRefresh != 10 {
			t.Errorf("intervals = %d/%d", s.BackgroundRefresh, s.ActiveRefresh)
		}
	})
	t.Run("re-orders instances stably and renumbers", func(t *testing.T) {
		s := Defaults()
		s.Instances = []InstanceProfile{
			{ID: "c", Order: 7}, {ID: "a", Order: 2}, {ID: "d", Order: 2}, {ID: "b", Order: -1},
		}
		s.Normalize()
		var ids []string
		for i, p := range s.Instances {
			ids = append(ids, p.ID)
			if p.Order != i {
				t.Errorf("instance %s has Order %d, want %d", p.ID, p.Order, i)
			}
		}
		if got := strings.Join(ids, ""); got != "badc" {
			t.Errorf("order = %s, want badc (ties keep their relative position)", got)
		}
	})
	t.Run("clears a dangling default instance", func(t *testing.T) {
		s := Defaults()
		s.Instances = []InstanceProfile{{ID: "a"}}
		s.DefaultInstanceID = "gone"
		s.Normalize()
		if s.DefaultInstanceID != "" {
			t.Errorf("DefaultInstanceID = %q", s.DefaultInstanceID)
		}
		s.DefaultInstanceID = "a"
		s.Normalize()
		if s.DefaultInstanceID != "a" {
			t.Errorf("valid DefaultInstanceID was cleared")
		}
	})
	t.Run("applied on load", func(t *testing.T) {
		f := newFile(t)
		writeRaw(t, f, `{"version":1,"instances":[{"id":"b","order":5},{"id":"a","order":1}],"defaultInstanceId":"zzz","backgroundRefresh":1,"activeRefresh":1,"language":"xx","appearance":"yy"}`)
		got, err := f.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got.Instances[0].ID != "a" || got.Instances[1].Order != 1 || got.DefaultInstanceID != "" ||
			got.BackgroundRefresh != 30 || got.ActiveRefresh != 10 || got.Language != LanguageAuto || got.Appearance != AppearanceSystem {
			t.Errorf("Load did not normalize: %+v", got)
		}
	})
}

func TestSettingsInstanceAndClone(t *testing.T) {
	s := Defaults()
	s.Instances = []InstanceProfile{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	if p := s.Instance("b"); p == nil || p.Name != "B" {
		t.Errorf("Instance(b) = %+v", p)
	}
	if s.Instance("zzz") != nil {
		t.Error("Instance of an unknown id must be nil")
	}
	s.Instance("a").Name = "A2" // a pointer into the slice
	if s.Instances[0].Name != "A2" {
		t.Error("Instance must point into the settings")
	}
	c := s.Clone()
	c.Instances[0].Name = "changed"
	c.Instances = append(c.Instances, InstanceProfile{ID: "c"})
	if s.Instances[0].Name != "A2" || len(s.Instances) != 2 {
		t.Error("Clone shares its instance list with the original")
	}
}

func TestExportInstancesNeverContainsKeys(t *testing.T) {
	profiles := []InstanceProfile{
		{ID: "secret-id-1", Name: "Home", BaseURL: "http://h:1/v8/management", Paused: true, Order: 3, CreatedAt: time.Now()},
		{ID: "secret-id-2", Name: "Work", BaseURL: "https://w/cpa/v8/management", CACertPEM: "-----BEGIN CERTIFICATE-----", ForwardAuthOnRedirect: true},
	}
	b, err := ExportInstances(profiles)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, forbidden := range []string{"secret-id", "managementKey", "management_key", "\"key\"", "password", "\"id\"", "paused", "createdAt"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("export contains %q:\n%s", forbidden, text)
		}
	}
	var generic struct {
		Format    string           `json:"format"`
		Version   int              `json:"version"`
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	if generic.Format != ExportFormat || generic.Version != 1 || len(generic.Instances) != 2 {
		t.Errorf("export = %+v", generic)
	}
	if _, ok := generic.Instances[0]["forwardAuthOnRedirect"]; ok {
		t.Error("false forwardAuthOnRedirect was not omitted")
	}
	if got := generic.Instances[1]["forwardAuthOnRedirect"]; got != true {
		t.Errorf("forwardAuthOnRedirect = %#v, want true", got)
	}
	for _, in := range generic.Instances {
		for k := range in {
			if k != "name" && k != "baseUrl" && k != "caCertPem" && k != "forwardAuthOnRedirect" && k != "icon" && k != "color" && k != "flavor" && k != "managerUrl" {
				t.Errorf("unexpected exported field %q", k)
			}
		}
	}
}

func TestExportParseRoundTrip(t *testing.T) {
	profiles := []InstanceProfile{
		{ID: "a", Name: "Home", BaseURL: "http://h:1/v8/management"},
		{ID: "b", Name: "Work", BaseURL: "https://w/cpa/v8/management", CACertPEM: "PEM DATA", ForwardAuthOnRedirect: true, Icon: "rocket", Color: "green"},
	}
	b, err := ExportInstances(profiles)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseInstances(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportedInstance{
		{Name: "Home", BaseURL: "http://h:1/v8/management"},
		{Name: "Work", BaseURL: "https://w/cpa/v8/management", CACertPEM: "PEM DATA", ForwardAuthOnRedirect: true, Icon: "rocket", Color: "green"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}

	empty, err := ExportInstances(nil)
	if err != nil || !bytes.Contains(empty, []byte(`"instances": []`)) {
		t.Errorf("export of nothing = %s, %v", empty, err)
	}
	if got, err := ParseInstances(empty); err != nil || len(got) != 0 || got == nil {
		t.Errorf("parse of an empty export = %#v, %v", got, err)
	}
}

func TestParseInstances(t *testing.T) {
	t.Run("skips entries without an address and trims", func(t *testing.T) {
		got, err := ParseInstances([]byte(`{"format":"cpa-bar.instances","version":1,"instances":[
			{"name":"  A  ","baseUrl":"  http://a  "},{"name":"no address","baseUrl":"  "},{"baseUrl":"http://b"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		want := []ImportedInstance{{Name: "A", BaseURL: "http://a"}, {BaseURL: "http://b"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("rejects", func(t *testing.T) {
		for name, in := range map[string]string{
			"wrong format":   `{"format":"something-else","version":1,"instances":[]}`,
			"missing format": `{"instances":[{"baseUrl":"http://a"}]}`,
			"settings file":  `{"version":1,"instances":[{"id":"a","baseUrl":"http://a"}]}`,
			"not json":       `hello`,
			"empty":          ``,
			"array":          `[]`,
		} {
			if got, err := ParseInstances([]byte(in)); err == nil {
				t.Errorf("%s: accepted, got %+v", name, got)
			}
		}
	})
}

func TestMemorySecrets(t *testing.T) {
	var s MemorySecrets
	var _ SecretStore = &s
	if _, err := s.Get("a"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get on empty store = %v", err)
	}
	if err := s.Set("a", "k1"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("a"); err != nil || v != "k1" {
		t.Errorf("Get = %q, %v", v, err)
	}
	if err := s.Set("a", "k2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("a"); v != "k2" {
		t.Errorf("overwrite lost: %q", v)
	}
	if err := s.Delete("missing"); err != nil {
		t.Errorf("Delete of a missing key = %v", err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get after Delete = %v", err)
	}

	boom := errors.New("keychain locked")
	s.Err = boom
	if _, err := s.Get("a"); !errors.Is(err, boom) {
		t.Errorf("Get with Err = %v", err)
	}
	if err := s.Set("a", "x"); !errors.Is(err, boom) {
		t.Errorf("Set with Err = %v", err)
	}
	if err := s.Delete("a"); !errors.Is(err, boom) {
		t.Errorf("Delete with Err = %v", err)
	}
}

// The battery style is gone: it shows as bars; unknown styles as the icon.
func TestNormalizeTrayStyle(t *testing.T) {
	for in, want := range map[string]string{"battery": TrayStyleBars, "bars": TrayStyleBars, "dots": TrayStyleIcon, "": TrayStyleIcon} {
		s := Defaults()
		s.TrayStyle = in
		s.Normalize()
		if s.TrayStyle != want {
			t.Errorf("%q normalizes to %q, want %q", in, s.TrayStyle, want)
		}
	}
}

func TestPassiveQuotaDefaultsOff(t *testing.T) {
	f := newFile(t)
	writeRaw(t, f, `{"version":1,"instances":[]}`)
	got, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PassiveQuota || Defaults().PassiveQuota {
		t.Fatal("passive quota must default off")
	}
}
