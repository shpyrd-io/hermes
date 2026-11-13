package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Options of a Program; the zero values are the container's.
type Options struct {
	Home         string            // Hermes' home, inside the volume
	Hermes       string            // the hermes binary
	Dashboard    string            // the dashboard's address on loopback
	DashboardArg []string          // how the dashboard is started
	GatewayArg   []string          // how the gateway is started
	Children     children          // runs the dashboard and the gateway
	Client       *http.Client      // reaches the providers
	Bases        map[string]string // a provider's base URL, for tests
	InstallKey   string            // an OpenRouter key given at install, if any
	Now          func() time.Time
}

// Program is the setup screen and, once set up, the door to the dashboard.
type Program struct {
	o     Options
	proxy *httputil.ReverseProxy
	dash  *url.URL
	web   http.Handler

	mu      sync.Mutex
	pending map[string]*pending
	running bool // the children were asked for
}

// pending is a checked key waiting for the model choice; the key never
// goes back to the browser.
type pending struct {
	Provider provider
	Base     string
	Key      string
	FromEnv  bool
	Models   []string
	Made     time.Time
}

// setup is what the person chose, kept beside Hermes' files.
type setup struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Base     string    `json:"base,omitempty"`
	DoneAt   time.Time `json:"doneAt"`
}

const setupFile = ".shpyrd/setup.json"

func New(o Options) *Program {
	if o.Hermes == "" {
		o.Hermes = "hermes"
	}
	if o.Dashboard == "" {
		o.Dashboard = "http://127.0.0.1:9119"
	}
	if len(o.DashboardArg) == 0 {
		o.DashboardArg = []string{"dashboard", "--host", "127.0.0.1", "--port", "9119", "--no-open", "--skip-build"}
	}
	if len(o.GatewayArg) == 0 {
		o.GatewayArg = []string{"gateway", "run", "--no-supervise", "--external-supervisor"}
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	dash, err := url.Parse(o.Dashboard)
	if err != nil {
		panic(err)
	}
	p := &Program{o: o, dash: dash, pending: map[string]*pending{}, web: webHandler()}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(dash)
			r.SetXForwarded()
			// The dashboard, bound to loopback with no login of its own,
			// trusts only loopback origins: a browser's Origin at the
			// project's address would be refused, websockets first. The
			// door checked that the Origin is the project's own (see
			// crossSite), so the dashboard is told the proxy's.
			if r.In.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", dash.Scheme+"://"+dash.Host)
			}
		},
		ErrorHandler: p.notUp,
	}
	return p
}

// crossSite says whether a request with an Origin header comes from a page
// that is not the project's own: the check the dashboard would make, kept
// here because the proxy rewrites the Origin.
func crossSite(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true
	}
	return !strings.EqualFold(u.Host, r.Host)
}

// Handler serves the setup and the dashboard.
func (p *Program) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+webPrefix+"/api/state", p.state)
	mux.HandleFunc("POST "+webPrefix+"/api/check", p.check)
	mux.HandleFunc("POST "+webPrefix+"/api/apply", p.apply)
	mux.Handle(webPrefix, p.web)
	mux.Handle(webPrefix+"/", p.web)
	mux.HandleFunc("/", p.door)
	return mux
}

// Start runs the children when the setup was done before.
func (p *Program) Start(ctx context.Context) {
	if _, ok := p.current(); ok {
		p.ensureChildren()
	} else {
		// Hermes' first command in a fresh home seeds its files and checks
		// its install, a minute's work: done now, not when the person
		// presses Finish.
		go func() {
			if err := p.hermes(ctx, "config", "get", "model"); err != nil {
				log.Printf("hermes-setup: warming Hermes' home: %v", err)
			}
		}()
	}
}

func (p *Program) current() (setup, bool) {
	b, err := os.ReadFile(filepath.Join(p.o.Home, setupFile))
	if err != nil {
		return setup{}, false
	}
	var s setup
	if json.Unmarshal(b, &s) != nil || s.Model == "" {
		return setup{}, false
	}
	return s, true
}

func (p *Program) ensureChildren() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running || p.o.Children == nil {
		return
	}
	p.running = true
	p.o.Children.Ensure("dashboard", p.o.DashboardArg)
	p.o.Children.Ensure("gateway", p.o.GatewayArg)
}

// dashboardUp says whether something listens at the dashboard's address.
func (p *Program) dashboardUp() bool {
	c, err := net.DialTimeout("tcp", p.dash.Host, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// door is every path but the setup's: the dashboard once set up and
// answering, the setup page otherwise.
func (p *Program) door(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.current(); !ok {
		p.toSetup(w, r)
		return
	}
	if crossSite(r) {
		http.Error(w, "a request from another site is refused", http.StatusForbidden)
		return
	}
	p.ensureChildren()
	p.proxy.ServeHTTP(w, r)
}

// toSetup sends a page to the setup, which opens Hermes by itself once
// the dashboard answers when the setup was done already. The platform's
// health checks, a plain GET of /, are told the door is up: the project
// is healthy while it waits to be set up. Any other program's request
// gets the answer in words.
func (p *Program) toSetup(w http.ResponseWriter, r *http.Request) {
	msg := "Hermes is not set up or not answering yet; open " + webPrefix
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		to := webPrefix
		if _, ok := p.current(); ok {
			to += "?starting"
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(msg + "\n"))
		return
	}
	w.Header().Set("Retry-After", "3")
	http.Error(w, msg, http.StatusServiceUnavailable)
}

// notUp is the dashboard not answering: the setup page says so and tries
// again until it does.
func (p *Program) notUp(w http.ResponseWriter, r *http.Request, err error) {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		log.Printf("hermes-setup: dashboard: %v", err)
	}
	p.toSetup(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
}

type providerView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	KeysAt string `json:"keysAt"`
	Custom bool   `json:"custom"`
}

type stateView struct {
	Configured     bool           `json:"configured"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	InstallKey     bool           `json:"installKey"`
	Providers      []providerView `json:"providers"`
	DashboardReady bool           `json:"dashboardReady"`
}

func (p *Program) stateView() stateView {
	v := stateView{InstallKey: p.o.InstallKey != ""}
	for _, pr := range providers {
		v.Providers = append(v.Providers, providerView{ID: pr.ID, Name: pr.Name, KeysAt: pr.KeysAt, Custom: pr.ID == "custom"})
	}
	if s, ok := p.current(); ok {
		v.Configured, v.Provider, v.Model = true, s.Provider, s.Model
		v.DashboardReady = p.dashboardUp()
	}
	return v
}

// state says what is set up and whether the dashboard answers.
func (p *Program) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, p.stateView())
}

// check tests the key against the provider and lists its models.
func (p *Program) check(w http.ResponseWriter, r *http.Request) {
	var in struct{ Provider, Key, Base string }
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "The request could not be read.")
		return
	}
	prov, ok := providerByID(in.Provider)
	if !ok {
		writeError(w, http.StatusBadRequest, "Choose a provider.")
		return
	}
	base := prov.Base
	if b := p.o.Bases[prov.ID]; b != "" {
		base = b
	}
	if prov.ID == "custom" {
		base = strings.TrimRight(strings.TrimSpace(in.Base), "/")
		if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
			writeError(w, http.StatusUnprocessableEntity, "The server's address needs to be a full URL, like https://llm.example.com/v1.")
			return
		}
	}
	key := strings.TrimSpace(in.Key)
	fromEnv := false
	if key == "" && prov.ID == "openrouter" && p.o.InstallKey != "" {
		key, fromEnv = p.o.InstallKey, true
	}
	if key == "" && prov.ID != "custom" {
		writeError(w, http.StatusUnprocessableEntity, "The key is needed.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	models, err := listModels(ctx, p.o.Client, prov, base, key)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, errRefused) {
			msg = prov.Name + " refused the key. Check it at " + prov.KeysAt + "."
		}
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if len(models) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "The key works, but "+prov.Name+" lists no models for it.")
		return
	}
	tok := p.remember(&pending{Provider: prov, Base: base, Key: key, FromEnv: fromEnv, Models: models, Made: p.o.Now()})
	chosen := ""
	if s, ok := p.current(); ok && s.Provider == prov.ID {
		chosen = s.Model
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "name": prov.Name, "models": models, "chosen": chosen})
}

func (p *Program) remember(pd *pending) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.pending {
		if p.o.Now().Sub(v.Made) > 20*time.Minute {
			delete(p.pending, k)
		}
	}
	p.pending[tok] = pd
	return tok
}

func (p *Program) take(tok string) *pending {
	p.mu.Lock()
	defer p.mu.Unlock()
	pd := p.pending[tok]
	delete(p.pending, tok)
	return pd
}

func (p *Program) keep(tok string, pd *pending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[tok] = pd
}

// apply writes the choice through Hermes' own config command and starts
// Hermes, or restarts the gateway, which reads the model at start.
func (p *Program) apply(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Model string }
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "The request could not be read.")
		return
	}
	pd := p.take(in.Token)
	if pd == nil {
		writeError(w, http.StatusUnprocessableEntity, "That check has expired. Enter the key again.")
		return
	}
	model := strings.TrimSpace(in.Model)
	known := false
	for _, m := range pd.Models {
		if m == model {
			known = true
			break
		}
	}
	if !known {
		p.keep(in.Token, pd) // the choice can be made again
		writeError(w, http.StatusUnprocessableEntity, "Choose one of the models listed.")
		return
	}
	sets := [][2]string{{"model.provider", pd.Provider.ID}, {"model.default", model}}
	switch {
	case pd.Provider.ID == "custom":
		sets = append(sets, [2]string{"model.base_url", pd.Base})
		if pd.Key != "" {
			sets = append(sets, [2]string{"model.api_key", pd.Key})
		}
	case pd.Provider.KeyVar != "" && !pd.FromEnv:
		sets = append(sets, [2]string{pd.Provider.KeyVar, pd.Key})
	}
	for _, kv := range sets {
		if err := p.hermes(r.Context(), "config", "set", kv[0], kv[1]); err != nil {
			p.keep(in.Token, pd)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s := setup{Provider: pd.Provider.ID, Model: model, DoneAt: p.o.Now()}
	if pd.Provider.ID == "custom" {
		s.Base = pd.Base
	}
	path := filepath.Join(p.o.Home, setupFile)
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = os.WriteFile(path, b, 0o644)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Hermes is configured, but the setup could not be recorded: "+err.Error())
			return
		}
	}
	log.Printf("hermes-setup: set up with %s, model %s", pd.Provider.Name, model)
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	if running {
		// The gateway reads the model at start. The channels and the rest
		// of Hermes' files are left to Hermes' own restart button: the
		// gateway writes those files itself at start, so a restart on
		// every change was a restart loop.
		p.o.Children.Restart("gateway")
	}
	p.ensureChildren()
	writeJSON(w, http.StatusOK, p.stateView())
}

// hermes runs one hermes command in the home, and says what it said when
// it fails. The first commands in a fresh home take a while.
func (p *Program) hermes(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.o.Hermes, args...)
	cmd.Dir = p.o.Home
	cmd.Env = append(os.Environ(), "HOME="+p.o.Home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		shown := args
		if len(shown) == 4 && strings.HasSuffix(strings.ToUpper(shown[2]), "_KEY") {
			shown = append(append([]string{}, shown[:3]...), "…")
		}
		return fmt.Errorf("hermes %s failed: %v: %s", strings.Join(shown, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
