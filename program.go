package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
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

//go:embed templates/*.html
var templates embed.FS

// Options of a Program; the zero values are the container's.
type Options struct {
	Home         string            // Hermes' home, the volume
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
	tmpl  *template.Template
	proxy *httputil.ReverseProxy
	dash  *url.URL

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
	p := &Program{o: o, dash: dash, pending: map[string]*pending{}}
	p.tmpl = template.Must(template.ParseFS(templates, "templates/*.html"))
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(dash)
			r.SetXForwarded()
		},
		ErrorHandler: p.notUp,
	}
	return p
}

// Handler serves the setup and the dashboard.
func (p *Program) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /setup", p.form)
	mux.HandleFunc("POST /setup/check", p.check)
	mux.HandleFunc("POST /setup/apply", p.apply)
	mux.HandleFunc("/", p.door)
	return mux
}

// Start runs the children when the setup was done before, and watches
// Hermes' files to restart the gateway when they change.
func (p *Program) Start(ctx context.Context) {
	if _, ok := p.current(); ok {
		p.ensureChildren()
	}
	go p.watch(ctx)
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

// watch restarts the gateway three quiet seconds after .env or config.yaml
// changed, by the setup, the dashboard's pages or a shell: the gateway
// reads them at start.
func (p *Program) watch(ctx context.Context) {
	files := []string{filepath.Join(p.o.Home, ".env"), filepath.Join(p.o.Home, "config.yaml")}
	stamp := func() string {
		var b strings.Builder
		for _, f := range files {
			if st, err := os.Stat(f); err == nil {
				fmt.Fprintf(&b, "%s:%d:%d;", f, st.ModTime().UnixNano(), st.Size())
			}
		}
		return b.String()
	}
	last := stamp()
	var changedAt time.Time
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if now := stamp(); now != last {
			last, changedAt = now, p.o.Now()
			continue
		}
		p.mu.Lock()
		running := p.running
		p.mu.Unlock()
		if !changedAt.IsZero() && running && p.o.Now().Sub(changedAt) >= 3*time.Second {
			changedAt = time.Time{}
			log.Printf("hermes-setup: Hermes' files changed, restarting the gateway")
			p.o.Children.Restart("gateway")
		}
	}
}

// door is every path but the setup's: the dashboard once set up.
func (p *Program) door(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.current(); !ok {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	p.ensureChildren()
	p.proxy.ServeHTTP(w, r)
}

// notUp is the dashboard not answering yet: a page that tries again.
func (p *Program) notUp(w http.ResponseWriter, r *http.Request, err error) {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		log.Printf("hermes-setup: dashboard: %v", err)
	}
	w.Header().Set("Refresh", "3")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = p.tmpl.ExecuteTemplate(w, "starting.html", nil)
}

type formData struct {
	Providers  []provider
	Provider   string
	Base       string
	InstallKey bool // an OpenRouter key came with the install
	Error      string
	Done       *setup
	// step two
	Token  string
	Models []string
	Name   string
	Chosen string
}

func (p *Program) render(w http.ResponseWriter, status int, d formData) {
	d.Providers = providers
	if d.Provider == "" {
		d.Provider = "openrouter"
	}
	d.InstallKey = p.o.InstallKey != ""
	if s, ok := p.current(); ok {
		d.Done = &s
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := p.tmpl.ExecuteTemplate(w, "setup.html", d); err != nil {
		log.Printf("hermes-setup: render: %v", err)
	}
}

func (p *Program) form(w http.ResponseWriter, r *http.Request) {
	p.render(w, http.StatusOK, formData{})
}

// check tests the key against the provider and shows its models.
func (p *Program) check(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	prov, ok := providerByID(r.Form.Get("provider"))
	if !ok {
		p.render(w, http.StatusBadRequest, formData{Error: "Choose a provider."})
		return
	}
	base := prov.Base
	if b := p.o.Bases[prov.ID]; b != "" {
		base = b
	}
	if prov.ID == "custom" {
		base = strings.TrimRight(strings.TrimSpace(r.Form.Get("base")), "/")
		if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
			p.render(w, http.StatusUnprocessableEntity, formData{Provider: prov.ID, Base: base, Error: "The server's address needs to be a full URL, like https://llm.example.com/v1."})
			return
		}
	}
	key := strings.TrimSpace(r.Form.Get("key"))
	fromEnv := false
	if key == "" && prov.ID == "openrouter" && p.o.InstallKey != "" {
		key, fromEnv = p.o.InstallKey, true
	}
	if key == "" && prov.ID != "custom" {
		p.render(w, http.StatusUnprocessableEntity, formData{Provider: prov.ID, Error: "The key is needed."})
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
		p.render(w, http.StatusUnprocessableEntity, formData{Provider: prov.ID, Base: base, Error: msg})
		return
	}
	if len(models) == 0 {
		p.render(w, http.StatusUnprocessableEntity, formData{Provider: prov.ID, Base: base, Error: "The key works, but " + prov.Name + " lists no models for it."})
		return
	}
	tok := p.remember(&pending{Provider: prov, Base: base, Key: key, FromEnv: fromEnv, Models: models, Made: p.o.Now()})
	chosen := ""
	if s, ok := p.current(); ok && s.Provider == prov.ID {
		chosen = s.Model
	}
	p.render(w, http.StatusOK, formData{Provider: prov.ID, Base: base, Token: tok, Models: models, Name: prov.Name, Chosen: chosen})
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

// apply writes the choice through Hermes' own config command and starts
// Hermes.
func (p *Program) apply(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pd := p.take(r.Form.Get("token"))
	if pd == nil {
		p.render(w, http.StatusUnprocessableEntity, formData{Error: "That check has expired. Enter the key again."})
		return
	}
	model := strings.TrimSpace(r.Form.Get("model"))
	known := false
	for _, m := range pd.Models {
		if m == model {
			known = true
			break
		}
	}
	if !known {
		tok := p.remember(pd)
		p.render(w, http.StatusUnprocessableEntity, formData{Provider: pd.Provider.ID, Base: pd.Base, Token: tok, Models: pd.Models, Name: pd.Provider.Name, Error: "Choose one of the models listed."})
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
			tok := p.remember(pd)
			p.render(w, http.StatusInternalServerError, formData{Provider: pd.Provider.ID, Base: pd.Base, Token: tok, Models: pd.Models, Name: pd.Provider.Name, Error: err.Error()})
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
			p.render(w, http.StatusInternalServerError, formData{Error: "Hermes is configured, but the setup could not be recorded: " + err.Error()})
			return
		}
	}
	log.Printf("hermes-setup: set up with %s, model %s", pd.Provider.Name, model)
	p.ensureChildren()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// hermes runs one hermes command in the home, and says what it said when
// it fails.
func (p *Program) hermes(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
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
