package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/licoy/cpa-bar/internal/cpamp"
	"github.com/licoy/cpa-bar/internal/cpamp/cpamptest"
	"github.com/licoy/cpa-bar/internal/store"
)

// An instance with CPA-Manager-Plus keeps its admin key beside the CPA key
// in the credential store, never in the settings, and drops it with the
// instance.
func TestManagerPlusInstance(t *testing.T) {
	f := newFakeCPA(t, "good", cred("a.json", "active"))
	mp := cpamptest.New(t, "cpamp_admin")
	e := newEnv(t)
	in := InstanceInput{Address: f.srv.URL, Key: "good", Flavor: store.FlavorManagerPlus, ManagerURL: mp.URL + "/management.html", ManagerKey: "cpamp_wrong"}
	if _, err := e.svc.AddInstance(bg, in); err == nil {
		t.Fatal("added with a refused admin key")
	}
	if st, _ := e.set.snapshot(); len(st.Instances) != 0 {
		t.Fatalf("saved %+v", st.Instances)
	}
	in.ManagerKey = "cpamp_admin"
	id, err := e.svc.AddInstance(bg, in)
	if err != nil {
		t.Fatal(err)
	}
	e.settle()
	st, _ := e.set.snapshot()
	if p := st.Instances[0]; !p.ManagerPlus() || p.ManagerURL != mp.URL {
		t.Errorf("profile %+v", p)
	}
	if k, _ := e.sec.Get(store.ManagerKeyID(id)); k != "cpamp_admin" {
		t.Errorf("stored admin key %q", k)
	}
	if c, err := e.svc.ManagerPlus(id); c == nil || err != nil {
		t.Errorf("no Manager-Plus client: %v", err)
	}
	b, _ := store.ExportInstances(st.Instances)
	if s := string(b); !strings.Contains(s, `"flavor": "manager-plus"`) || strings.Contains(s, "cpamp_admin") || strings.Contains(s, "good") {
		t.Errorf("export %s", s)
	}

	// An edit without the admin key keeps it; going back to CPA alone
	// drops it.
	in.ManagerKey = ""
	in.Key = ""
	if err := e.svc.UpdateInstance(id, in); err != nil {
		t.Fatal(err)
	}
	if k, _ := e.sec.Get(store.ManagerKeyID(id)); k != "cpamp_admin" {
		t.Errorf("admin key after an edit %q", k)
	}
	in.Flavor = store.FlavorOfficial
	if err := e.svc.UpdateInstance(id, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(store.ManagerKeyID(id)); err == nil {
		t.Error("admin key kept for CPA alone")
	}
	if c, _ := e.svc.ManagerPlus(id); c != nil {
		t.Error("Manager-Plus client for CPA alone")
	}

	in.Flavor, in.ManagerKey = store.FlavorManagerPlus, "cpamp_admin"
	if err := e.svc.UpdateInstance(id, in); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveInstance(id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(store.ManagerKeyID(id)); err == nil {
		t.Error("admin key kept after removal")
	}
}

func TestSwitchFromManagerPlusKeepsCredentialAndProfileWhenDeleteFails(t *testing.T) {
	f := newFakeCPA(t, "good")
	managerID := store.ManagerKeyID("i0")
	secrets := &faultSecrets{m: map[string]string{"i0": "good", managerID: "admin"}}
	e := newEnvWith(t, func(o *Options) {
		o.Secrets = secrets
		p := &o.Settings.(*fakeSettings).s.Instances[0]
		p.Flavor = store.FlavorManagerPlus
		p.ManagerURL = "http://manager.invalid"
	}, f)
	deleteErr := errors.New("manager key delete failed")
	deleteCalls := 0
	secrets.deleteHook = func(id string) error {
		deleteCalls++
		delete(secrets.m, id)
		if deleteCalls == 1 {
			return deleteErr
		}
		return nil
	}
	input := InstanceInput{Name: "N0", Address: f.srv.URL, Flavor: store.FlavorOfficial}
	if err := e.svc.UpdateInstance("i0", input); !errors.Is(err, deleteErr) {
		t.Fatalf("err = %v", err)
	}
	if !e.svc.Settings().Instances[0].ManagerPlus() || secrets.m[managerID] != "admin" {
		t.Fatalf("failed switch changed profile or credential: %+v, %v", e.svc.Settings().Instances[0], secrets.m)
	}
	if err := e.svc.UpdateInstance("i0", input); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if e.svc.Settings().Instances[0].ManagerPlus() {
		t.Error("profile still uses Manager-Plus after retry")
	}
	if _, ok := secrets.m[managerID]; ok {
		t.Error("Manager-Plus credential remains after retry")
	}
}

func TestClientFactoriesForwardAuthorizationWhenEnabled(t *testing.T) {
	var auth []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v8/management/config":
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/credentials":
			_, _ = w.Write([]byte(`{"files":[]}`))
		case "/usage-service/config":
			_, _ = w.Write([]byte(`{"config":{"cpaConnection":{"cpaBaseUrl":"http://cpa:8317"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	c, err := DefaultClientFactory(store.InstanceProfile{
		BaseURL: redirector.URL, ForwardAuthOnRedirect: true,
	}, "cpa-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Probe(bg); err != nil {
		t.Fatal(err)
	}
	pc, err := plusClient(store.InstanceProfile{
		Flavor: store.FlavorManagerPlus, ManagerURL: redirector.URL, ForwardAuthOnRedirect: true,
	}, "manager-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.CPAAddress(bg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Bearer cpa-key", "Bearer cpa-key", "Bearer manager-key"}; !slices.Equal(auth, want) {
		t.Errorf("redirect authorization = %v, want %v", auth, want)
	}
}

// Detection reads /health: Manager-Plus at the address, or CPA.
func TestDetect(t *testing.T) {
	f := newFakeCPA(t, "good")
	mp := cpamptest.New(t, "k")
	e := newEnv(t)
	if d, err := e.svc.Detect(bg, mp.URL+"/management.html", ""); err != nil || !d.ManagerPlus {
		t.Errorf("Manager-Plus detected as %+v, %v", d, err)
	}
	if d, err := e.svc.Detect(bg, f.srv.URL, ""); err != nil || d.ManagerPlus {
		t.Errorf("CPA detected as %+v, %v", d, err)
	}
	if !slices.Contains(mp.Paths(), "GET /health") || slices.ContainsFunc(f.requestLog(), func(s string) bool { return s != "GET /health" }) {
		t.Errorf("detection asked %q and %q", mp.Paths(), f.requestLog())
	}
	mp.CPAAddress = "http://cpa:8317"
	if a, err := e.svc.ManagerCPAAddress(bg, cpamp.Options{BaseURL: mp.URL, AdminKey: "k"}); err != nil || a != "http://cpa:8317" {
		t.Errorf("CPA address %q, %v", a, err)
	}
}
