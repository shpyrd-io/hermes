package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a log that tests can read while the supervisor writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestASupervisedProcessIsKeptRunningAndRestartedOnRequest(t *testing.T) {
	dir := t.TempDir()
	// A "hermes" whose dashboard says hello, then sleeps; its gateway exits
	// at once, to be started again.
	script := `#!/bin/sh
case "$1" in
  dashboard) echo "hello from $$"; exec sleep 30 ;;
  gateway) echo "gateway ran"; exit 1 ;;
esac
`
	bin := filepath.Join(dir, "hermes")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	s := newSupervisor(bin, dir, out)
	s.Ensure("dashboard", []string{"dashboard"})
	s.Ensure("gateway", []string{"gateway"})
	s.Ensure("dashboard", []string{"dashboard"}) // a second Ensure is nothing
	waitFor(t, "the dashboard's hello", func() bool { return strings.Count(out.String(), "hello from") == 1 })
	waitFor(t, "the gateway's second run", func() bool { return strings.Count(out.String(), "gateway ran") >= 2 })

	s.Restart("dashboard")
	waitFor(t, "the dashboard's second hello", func() bool { return strings.Count(out.String(), "hello from") == 2 })
	if !strings.Contains(out.String(), "dashboard: restarting") {
		t.Fatalf("log: %s", out.String())
	}

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop did not return")
	}
	if strings.Count(out.String(), "hello from") != 2 {
		t.Fatalf("the dashboard was started again after Stop: %s", out.String())
	}
}
