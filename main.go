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
	"os/exec"
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
	if os.Getpid() == 1 && os.Getenv("HERMES_SETUP_INIT") == "" {
		os.Exit(runAsInit())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
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

// runAsInit is what PID 1 does: run the program again as a child and be
// its init. Processes Hermes leaves behind are re-parented to PID 1 and
// must be waited for, or they stay zombies; but a PID 1 that waits for
// any child races the child's own waits (a `hermes config set` whose
// exit was taken by the reaper fails with "no child processes"). As a
// separate process, the init waits for everything and the program waits
// for its own children only. Signals are passed on; the init ends with
// the program's status.
func runAsInit() int {
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(os.Environ(), "HERMES_SETUP_INIT=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("hermes-setup: init: %v", err)
		return 1
	}
	child := cmd.Process.Pid
	sig := make(chan os.Signal, 8)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGCHLD)
	sig <- syscall.SIGCHLD // a child gone before Notify sent nothing
	for s := range sig {
		if s != syscall.SIGCHLD {
			_ = cmd.Process.Signal(s)
			continue
		}
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
			if pid == child {
				if ws.Signaled() {
					return 128 + int(ws.Signal())
				}
				return ws.ExitStatus()
			}
		}
	}
	return 0
}
