package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// children runs Hermes' long-lived processes. The Program asks for them
// by name once setup is done, and for a restart when Hermes' files change.
type children interface {
	Ensure(name string, argv []string)
	Restart(name string)
	Stop()
}

// supervisor keeps each named process running: a process that exits is
// started again after a pause that grows to half a minute.
type supervisor struct {
	hermes string
	home   string
	out    io.Writer

	mu    sync.Mutex
	procs map[string]*proc
	wg    sync.WaitGroup
}

type proc struct {
	name    string
	argv    []string
	restart chan struct{}
	stop    chan struct{}
}

func newSupervisor(hermes, home string, out io.Writer) *supervisor {
	return &supervisor{hermes: hermes, home: home, out: out, procs: map[string]*proc{}}
}

func (s *supervisor) Ensure(name string, argv []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.procs[name]; ok {
		return
	}
	p := &proc{name: name, argv: argv, restart: make(chan struct{}, 1), stop: make(chan struct{})}
	s.procs[name] = p
	s.wg.Add(1)
	go s.keep(p)
}

func (s *supervisor) Restart(name string) {
	s.mu.Lock()
	p := s.procs[name]
	s.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case p.restart <- struct{}{}:
	default:
	}
}

// Stop ends every process and waits for them.
func (s *supervisor) Stop() {
	s.mu.Lock()
	for name, p := range s.procs {
		close(p.stop)
		delete(s.procs, name)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *supervisor) keep(p *proc) {
	defer s.wg.Done()
	pause := time.Second
	for {
		cmd := exec.Command(s.hermes, p.argv...)
		cmd.Dir = s.home
		cmd.Env = append(os.Environ(), "HOME="+s.home)
		cmd.Stdout = s.prefixed(p.name)
		cmd.Stderr = cmd.Stdout
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		started := time.Now()
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(s.out, "%s: cannot start: %v\n", p.name, err)
		} else {
			fmt.Fprintf(s.out, "%s: started (pid %d)\n", p.name, cmd.Process.Pid)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				fmt.Fprintf(s.out, "%s: exited: %v\n", p.name, exitWords(err))
			case <-p.restart:
				fmt.Fprintf(s.out, "%s: restarting\n", p.name)
				end(cmd, done)
				pause = time.Second
				continue
			case <-p.stop:
				end(cmd, done)
				return
			}
			if time.Since(started) > time.Minute {
				pause = time.Second
			}
		}
		select {
		case <-time.After(pause):
		case <-p.restart:
		case <-p.stop:
			return
		}
		if pause < 30*time.Second {
			pause *= 2
		}
	}
}

// end asks the process group to finish and kills it after ten seconds.
func end(cmd *exec.Cmd, done <-chan error) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func exitWords(err error) string {
	if err == nil {
		return "status 0"
	}
	return err.Error()
}

// prefixed writes the process's output to the log a line at a time, with
// the process's name in front.
func (s *supervisor) prefixed(name string) io.Writer {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			fmt.Fprintf(s.out, "%s: %s\n", name, sc.Text())
		}
	}()
	return pw
}
