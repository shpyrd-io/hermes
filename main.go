// hermes-setup is the web process of the Hermes Agent project on shpyrd.
//
// Until the person has chosen a provider, a key and a model, every request
// shows the setup screen. Afterwards it runs Hermes' own dashboard and
// gateway as children and serves the dashboard, which listens on loopback
// only: the platform's sign-in is the one login. The setup stays at /setup
// to reconfigure.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(0)
	home := envOr("HERMES_HOME", "/opt/data")
	port := envOr("PORT", "8080")
	dash := envOr("HERMES_DASHBOARD_PORT", "9119")
	if _, err := strconv.Atoi(dash); err != nil {
		log.Fatalf("HERMES_DASHBOARD_PORT %q is not a port", dash)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if os.Getpid() == 1 {
		go reapOrphans(ctx)
	}
	sup := newSupervisor(envOr("HERMES_BIN", "hermes"), home, os.Stderr)
	p := New(Options{
		Home:         home,
		Dashboard:    "http://127.0.0.1:" + dash,
		Children:     sup,
		InstallKey:   os.Getenv("OPENROUTER_API_KEY"),
		DashboardArg: []string{"dashboard", "--host", "127.0.0.1", "--port", dash, "--no-open", "--skip-build"},
	})
	srv := &http.Server{Addr: ":" + port, Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	p.Start(ctx)
	log.Printf("hermes-setup: listening on :%s, Hermes home %s", port, home)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("hermes-setup: %v", err)
	}
	sup.Stop()
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// reapOrphans is PID 1's duty: processes Hermes leaves behind are
// re-parented to us and must be waited for, or they stay zombies. Our own
// children are waited for by the supervisor; when this loop gets to one
// first, the supervisor sees the wait fail and treats it as an exit.
func reapOrphans(ctx context.Context) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			for {
				var ws syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
		}
	}
}
