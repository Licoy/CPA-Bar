package core

import (
	"slices"
	"strings"
	"testing"

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
	if a, err := e.svc.ManagerCPAAddress(bg, mp.URL, "k", ""); err != nil || a != "http://cpa:8317" {
		t.Errorf("CPA address %q, %v", a, err)
	}
}
