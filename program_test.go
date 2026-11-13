package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChildren records what the program asked for.
type fakeChildren struct {
	mu       sync.Mutex
	ensured  []string
	restarts []string
}

func (f *fakeChildren) Ensure(name string, argv []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, name+" "+strings.Join(argv, " "))
}
func (f *fakeChildren) Restart(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, name)
}
func (f *fakeChildren) Stop() {}

// fakeHermes is a hermes binary that records its calls in calls.log and,
// for config set, touches the file Hermes would write.
func fakeHermes(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "hermes")
	script := `#!/bin/sh
echo "$*" >> "$HOME/calls.log"
case "$1 $2" in
  "config set")
    case "$3" in
      *.*) touch "$HOME/config.yaml" ;;
      *) touch "$HOME/.env" ;;
    esac
    [ "$3" = "model.default" ] && [ "$4" = "boom" ] && { echo "no such model"; exit 2; }
    ;;
esac
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// modelsServer answers /models like OpenRouter: a list for the right key,
// 401 otherwise.
func modelsServer(t *testing.T, key string, ids ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key && r.Header.Get("x-api-key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var data []map[string]string
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

type rig struct {
	t        *testing.T
	p        *Program
	srv      *httptest.Server
	home     string
	children *fakeChildren
	dash     *httptest.Server
	client   *http.Client
}

func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	home := t.TempDir()
	bin := t.TempDir()
	opts.Home = home
	opts.Hermes = fakeHermes(t, bin)
	ch := &fakeChildren{}
	opts.Children = ch
	dash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "dashboard "+r.URL.Path+" via "+r.Header.Get("X-Forwarded-Host")+" origin "+r.Header.Get("Origin"))
	}))
	t.Cleanup(dash.Close)
	opts.Dashboard = dash.URL
	p := New(opts)
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &rig{t: t, p: p, srv: srv, home: home, children: ch, dash: dash, client: client}
}

func (r *rig) get(path string) (int, string) {
	r.t.Helper()
	res, err := r.client.Get(r.srv.URL + path)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (r *rig) post(path string, form url.Values) (int, string, http.Header) {
	r.t.Helper()
	res, err := r.client.PostForm(r.srv.URL+path, form)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func (r *rig) calls() string {
	b, _ := os.ReadFile(filepath.Join(r.home, "calls.log"))
	return string(b)
}

var tokenRe = regexp.MustCompile(`name="token" value="([0-9a-f]+)"`)

func TestBeforeSetupEveryPathIsTheSetup(t *testing.T) {
	r := newRig(t, Options{})
	for _, path := range []string{"/", "/sessions", "/api/anything"} {
		status, _ := r.get(path)
		if status != http.StatusSeeOther {
			t.Errorf("%s: %d, want a redirect to the setup", path, status)
		}
	}
	status, body := r.get("/setup")
	if status != 200 || !strings.Contains(body, "Check the key") || !strings.Contains(body, "OpenRouter") {
		t.Fatalf("setup page: %d %q", status, body)
	}
	if len(r.children.ensured) != 0 {
		t.Errorf("nothing should run before setup, got %v", r.children.ensured)
	}
}

func TestARefusedKeyIsSaidInWords(t *testing.T) {
	ms := modelsServer(t, "good", "a/one")
	defer ms.Close()
	r := newRig(t, Options{Bases: map[string]string{"openrouter": ms.URL}})
	status, body, _ := r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {"wrong-key-xyz"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "OpenRouter refused the key") {
		t.Fatalf("%d %q", status, body)
	}
	if strings.Contains(body, "wrong-key-xyz") {
		t.Error("the key must not be echoed")
	}
	status, body, _ = r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {""}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "The key is needed") {
		t.Fatalf("%d %q", status, body)
	}
}

func TestTheWholeSetup(t *testing.T) {
	ms := modelsServer(t, "sk-or-1", "zeta/z", "anthropic/claude", "minimax/m3")
	defer ms.Close()
	r := newRig(t, Options{Bases: map[string]string{"openrouter": ms.URL}})

	status, body, _ := r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {"sk-or-1"}})
	if status != 200 || !strings.Contains(body, "lists 3 models") {
		t.Fatalf("check: %d %q", status, body)
	}
	if strings.Contains(body, "sk-or-1") {
		t.Fatal("the key went back to the browser")
	}
	if i, j := strings.Index(body, "anthropic/claude"), strings.Index(body, "zeta/z"); i < 0 || j < 0 || i > j {
		t.Fatalf("models should be listed sorted: %q", body)
	}
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no token in %q", body)
	}

	status, body, _ = r.post("/setup/apply", url.Values{"token": {m[1]}, "model": {"not/listed"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose one of the models listed") {
		t.Fatalf("unknown model: %d %q", status, body)
	}
	m = tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the second chance has no token")
	}

	status, _, hdr := r.post("/setup/apply", url.Values{"token": {m[1]}, "model": {"minimax/m3"}})
	if status != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("apply: %d %v", status, hdr)
	}
	want := "config set model.provider openrouter\nconfig set model.default minimax/m3\nconfig set OPENROUTER_API_KEY sk-or-1\n"
	if got := r.calls(); got != want {
		t.Fatalf("hermes calls:\n%s\nwant:\n%s", got, want)
	}
	s, ok := r.p.current()
	if !ok || s.Provider != "openrouter" || s.Model != "minimax/m3" {
		t.Fatalf("setup record: %+v %v", s, ok)
	}
	if got := strings.Join(r.children.ensured, "; "); !strings.Contains(got, "dashboard dashboard --host 127.0.0.1") || !strings.Contains(got, "gateway gateway run") {
		t.Fatalf("children: %s", got)
	}

	// Now the door is the dashboard.
	status, body = r.get("/sessions")
	if status != 200 || !strings.Contains(body, "dashboard /sessions via 127.0.0.1") {
		t.Fatalf("door: %d %q", status, body)
	}
	// And the setup still opens, saying what is set.
	status, body = r.get("/setup")
	if status != 200 || !strings.Contains(body, "set up with <strong>openrouter</strong>") {
		t.Fatalf("setup again: %d %q", status, body)
	}
	// A second check with the same provider preselects the current model.
	_, body, _ = r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {"sk-or-1"}})
	if !strings.Contains(body, `value="minimax/m3" selected`) {
		t.Fatalf("current model not preselected: %q", body)
	}
}

func TestAKeyGivenAtInstallNeedsNoTyping(t *testing.T) {
	ms := modelsServer(t, "sk-install", "a/one")
	defer ms.Close()
	r := newRig(t, Options{Bases: map[string]string{"openrouter": ms.URL}, InstallKey: "sk-install"})
	_, body := r.get("/setup")
	if !strings.Contains(body, "given when the project was installed") {
		t.Fatalf("no word of the install key: %q", body)
	}
	status, body, _ := r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {""}})
	if status != 200 {
		t.Fatalf("check with the install key: %d %q", status, body)
	}
	m := tokenRe.FindStringSubmatch(body)
	r.post("/setup/apply", url.Values{"token": {m[1]}, "model": {"a/one"}})
	if got := r.calls(); strings.Contains(got, "OPENROUTER_API_KEY") {
		t.Fatalf("the install key is in the environment already; hermes calls:\n%s", got)
	}
}

func TestACustomServerTakesItsAddress(t *testing.T) {
	ms := modelsServer(t, "", "llama")
	defer ms.Close()
	r := newRig(t, Options{})
	status, body, _ := r.post("/setup/check", url.Values{"provider": {"custom"}, "base": {"not a url"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "full URL") {
		t.Fatalf("%d %q", status, body)
	}
	status, body, _ = r.post("/setup/check", url.Values{"provider": {"custom"}, "base": {ms.URL + "/"}})
	if status != 200 {
		t.Fatalf("%d %q", status, body)
	}
	m := tokenRe.FindStringSubmatch(body)
	r.post("/setup/apply", url.Values{"token": {m[1]}, "model": {"llama"}})
	want := "config set model.provider custom\nconfig set model.default llama\nconfig set model.base_url " + ms.URL + "\n"
	if got := r.calls(); got != want {
		t.Fatalf("hermes calls:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnthropicIsAskedWithItsOwnHeaders(t *testing.T) {
	ms := modelsServer(t, "sk-ant", "claude-x")
	defer ms.Close()
	r := newRig(t, Options{Bases: map[string]string{"anthropic": ms.URL}})
	status, body, _ := r.post("/setup/check", url.Values{"provider": {"anthropic"}, "key": {"sk-ant"}})
	if status != 200 || !strings.Contains(body, "claude-x") {
		t.Fatalf("%d %q", status, body)
	}
}

func TestAFailedHermesCommandIsSaid(t *testing.T) {
	ms := modelsServer(t, "k", "boom")
	defer ms.Close()
	r := newRig(t, Options{Bases: map[string]string{"openrouter": ms.URL}})
	_, body, _ := r.post("/setup/check", url.Values{"provider": {"openrouter"}, "key": {"k"}})
	m := tokenRe.FindStringSubmatch(body)
	status, body, _ := r.post("/setup/apply", url.Values{"token": {m[1]}, "model": {"boom"}})
	if status != http.StatusInternalServerError || !strings.Contains(body, "hermes config set model.default boom failed") || !strings.Contains(body, "no such model") {
		t.Fatalf("%d %q", status, body)
	}
	if _, ok := r.p.current(); ok {
		t.Fatal("a failed setup must not be recorded")
	}
}

func TestAnExpiredTokenAsksAgain(t *testing.T) {
	r := newRig(t, Options{})
	status, body, _ := r.post("/setup/apply", url.Values{"token": {"nope"}, "model": {"x"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "expired") {
		t.Fatalf("%d %q", status, body)
	}
}

func TestTheDashboardSeesTheProxysOriginAndOtherSitesAreRefused(t *testing.T) {
	r := newRig(t, Options{})
	_ = os.MkdirAll(filepath.Join(r.home, ".shpyrd"), 0o755)
	_ = os.WriteFile(filepath.Join(r.home, setupFile), []byte(`{"provider":"openrouter","model":"m"}`), 0o644)
	own := strings.TrimPrefix(r.srv.URL, "http://")
	for _, c := range []struct {
		origin string
		status int
		want   string
	}{
		{"", 200, "origin \n"},
		{"http://" + own, 200, "origin " + r.dash.URL},
		{"http://" + strings.ToUpper(own), 200, "origin " + r.dash.URL},
		{"null", 200, "origin " + r.dash.URL},
		{"https://evil.example", 403, "another site"},
		{"garbage", 403, "another site"},
	} {
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/api/ws", nil)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		res, err := r.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != c.status || !strings.Contains(string(b)+"\n", c.want) {
			t.Errorf("origin %q: %d %q, want %d containing %q", c.origin, res.StatusCode, b, c.status, c.want)
		}
	}
}

func TestTheDoorWaitsForTheDashboard(t *testing.T) {
	r := newRig(t, Options{})
	if err := os.MkdirAll(filepath.Join(r.home, ".shpyrd"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(r.home, setupFile), []byte(`{"provider":"openrouter","model":"m"}`), 0o644)
	r.dash.Close() // nobody on the dashboard's port
	status, body := r.get("/")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "Hermes is starting") {
		t.Fatalf("%d %q", status, body)
	}
}

func TestStartRunsTheChildrenWhenSetUpBefore(t *testing.T) {
	r := newRig(t, Options{})
	_ = os.MkdirAll(filepath.Join(r.home, ".shpyrd"), 0o755)
	_ = os.WriteFile(filepath.Join(r.home, setupFile), []byte(`{"provider":"openrouter","model":"m"}`), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.p.Start(ctx)
	if len(r.children.ensured) != 2 {
		t.Fatalf("children: %v", r.children.ensured)
	}
}

func TestAChangedEnvRestartsTheGatewayAfterAQuietMoment(t *testing.T) {
	r := newRig(t, Options{})
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	r.p.o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	_ = os.MkdirAll(filepath.Join(r.home, ".shpyrd"), 0o755)
	_ = os.WriteFile(filepath.Join(r.home, setupFile), []byte(`{"provider":"openrouter","model":"m"}`), 0o644)
	_ = os.WriteFile(filepath.Join(r.home, ".env"), []byte("A=1\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.p.Start(ctx)
	time.Sleep(1500 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(r.home, ".env"), []byte("A=1\nTELEGRAM_BOT_TOKEN=t\n"), 0o600)
	time.Sleep(1500 * time.Millisecond)
	r.children.mu.Lock()
	early := len(r.children.restarts)
	r.children.mu.Unlock()
	if early != 0 {
		t.Fatal("restarted before the quiet moment")
	}
	mu.Lock()
	now = now.Add(5 * time.Second)
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.children.mu.Lock()
		n := len(r.children.restarts)
		r.children.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway restarts: %d, want 1", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
