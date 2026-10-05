package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

func profile(key, url string) *js.Object {
	return js.O("api_url", url, "api_key", key, "workspace", "W", "agent", "A", "created_at", "2026-10-01T00:00:00Z")
}

func TestDirPrecedence(t *testing.T) {
	if p := Path(Env{"GC_CONFIG_DIR": "/x", "XDG_CONFIG_HOME": "/y", "HOME": "/h"}); p != "/x/config.json" {
		t.Fatal(p)
	}
	if p := Path(Env{"XDG_CONFIG_HOME": "/y", "HOME": "/h"}); p != "/y/groundcontrol/config.json" {
		t.Fatal(p)
	}
	if p := Path(Env{"HOME": "/h"}); p != "/h/.config/groundcontrol/config.json" {
		t.Fatal(p)
	}
}

func TestSaveIsPrivateAtomicAndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	env := Env{"GC_CONFIG_DIR": dir}
	cfg := &Config{Current: "b", Profiles: js.O("b", profile("gc_live_bbbbbbbbbbbb2222", "https://b"), "a", profile("gc_live_aaaaaaaaaaaa1111", "https://a"))}
	path, err := Save(cfg, env)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir %o", st.Mode().Perm())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("file %o", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
	b, _ := os.ReadFile(path)
	// The TS layout: JSON.stringify(cfg, null, 2) + "\n", profiles in file order.
	if !strings.HasPrefix(string(b), "{\n  \"current\": \"b\",\n  \"profiles\": {\n    \"b\": {\n      \"api_url\": \"https://b\",") || !strings.HasSuffix(string(b), "}\n") {
		t.Fatalf("layout:\n%s", b)
	}
	got, err := Load(env)
	if err != nil || got.Current != "b" || strings.Join(got.Profiles.Keys(), ",") != "b,a" {
		t.Fatalf("load: %v %+v", err, got)
	}
	cfg.Current = ""
	Save(cfg, env)
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"current": null`) {
		t.Fatalf("null current: %s", b)
	}
}

func TestLoadMissingAndBroken(t *testing.T) {
	dir := t.TempDir()
	env := Env{"GC_CONFIG_DIR": dir}
	cfg, err := Load(env)
	if err != nil || cfg.Current != "" || cfg.Profiles.Len() != 0 {
		t.Fatal("missing file should be an empty config")
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{nope"), 0o600)
	if _, err := Load(env); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err %v", err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	cfg := &Config{Current: "a", Profiles: js.O("a", profile("gc_live_profileA0000", "https://a.example/api/v1"), "b", profile("gc_live_profileB0000", "https://b.example/api/v1/"))}
	r, _ := Resolve(Flags{}, Env{}, cfg)
	if r.APIKey != "gc_live_profileA0000" || r.KeySource != "profile" || r.Profile != "a" || r.APIURL != "https://a.example/api/v1" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{}, Env{"GC_API_KEY": "env"}, cfg); r.APIKey != "env" || r.KeySource != "env" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{Token: "flag"}, Env{"GC_API_KEY": "env"}, cfg); r.APIKey != "flag" || r.KeySource != "flag" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{}, Env{"GC_PROFILE": "b"}, cfg); r.APIKey != "gc_live_profileB0000" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{Profile: "a"}, Env{"GC_PROFILE": "b"}, cfg); r.APIKey != "gc_live_profileA0000" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{Profile: "b"}, Env{}, cfg); r.APIURL != "https://b.example/api/v1" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{Profile: "b"}, Env{"GC_API_URL": "http://env/api/v1"}, cfg); r.APIURL != "http://env/api/v1" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{Profile: "b", APIURL: "http://flag"}, Env{"GC_API_URL": "http://env"}, cfg); r.APIURL != "http://flag" {
		t.Fatalf("%+v", r)
	}
	if r, _ := Resolve(Flags{}, Env{}, &Config{Profiles: js.NewObject()}); r.APIURL != DefaultAPIURL || r.APIKey != "" || r.KeySource != "none" {
		t.Fatalf("%+v", r)
	}
	// An explicitly named unknown profile is an error, never a silent fallback.
	if _, err := Resolve(Flags{Profile: "zzz"}, Env{}, cfg); err == nil || !strings.Contains(err.Error(), `Unknown profile "zzz". Known: a, b.`) {
		t.Fatalf("err %v", err)
	}
	if _, err := Resolve(Flags{}, Env{"GC_PROFILE": "zzz"}, cfg); err == nil {
		t.Fatal("GC_PROFILE unknown")
	}
	if _, err := RequireKey(Resolved{APIURL: "http://x"}); err == nil || !strings.Contains(err.Error(), "(no API key)") {
		t.Fatalf("err %v", err)
	}
}

func TestRedactAndSlugify(t *testing.T) {
	if RedactKey("gc_live_abcdefghijklmnop1234") != "gc_live_…1234" || RedactKey("short") != "…" || RedactKey("") != "(none)" {
		t.Fatal("redact")
	}
	cases := map[string]string{"MakersLab GmbH": "makerslab-gmbh", "Jörg’s Büro": "jorg-s-buro", "!!!": "default", "Straße Łódź": "stra-e-odz", "Ｆｕｌｌ": "full",
		"a very long workspace name that goes on and on and on": "a-very-long-workspace-name-that-goes-on-", "Ĳssel Ŀa ŉ": "ijssel-l-a-n"}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
}
