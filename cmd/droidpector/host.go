package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/platform"
)

// coreProcess is a supervised core child process.
type coreProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	ready ready
	done  chan struct{}
	err   error
}

// startCore launches this executable in --core mode inside the process group
// and waits for its ready line.
func startCore(procs *platform.ProcessGroup) (*coreProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "--core")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := procs.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting the droidpector core: %w", err)
	}
	cp := &coreProcess{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		cp.err = cmd.Wait()
		close(cp.done)
	}()
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "{") {
				select {
				case lines <- sc.Text():
				default:
				}
			}
		}
	}()
	select {
	case l := <-lines:
		if err := json.Unmarshal([]byte(l), &cp.ready); err != nil {
			cp.kill()
			return nil, fmt.Errorf("the core sent an invalid handshake: %w", err)
		}
		return cp, nil
	case <-cp.done:
		return nil, fmt.Errorf("the droidpector core exited during startup (%v); see logs\\app.log", cp.err)
	case <-time.After(60 * time.Second):
		cp.kill()
		return nil, fmt.Errorf("the droidpector core did not start within 60 seconds")
	}
}

// shutdown asks the core to exit (stdin EOF) and waits, then kills it.
func (c *coreProcess) shutdown(grace time.Duration) {
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(grace):
		c.kill()
	}
}

func (c *coreProcess) kill() {
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

// supervisor keeps a core running and restarts it after a crash (at most
// 3 times per 5 minutes), notifying the window of URL changes.
type supervisor struct {
	procs    *platform.ProcessGroup
	onURL    func(url string)
	onFailed func(msg string)

	mu      sync.Mutex
	cur     *coreProcess
	closing bool
	starts  []time.Time
}

func (s *supervisor) start() error {
	cp, err := startCore(s.procs)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = cp
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	go s.watch(cp)
	return nil
}

func (s *supervisor) watch(cp *coreProcess) {
	<-cp.done
	s.mu.Lock()
	closing := s.closing
	var recent []time.Time
	for _, t := range s.starts {
		if time.Since(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	s.starts = recent
	s.mu.Unlock()
	if closing {
		return
	}
	if len(recent) >= 4 {
		s.onFailed("The droidpector core stopped repeatedly. Use \"Save diagnostic bundle\" from the logs folder and restart the application.")
		return
	}
	if err := s.start(); err != nil {
		s.onFailed(err.Error())
		return
	}
	s.mu.Lock()
	url := s.cur.ready.URL
	s.mu.Unlock()
	s.onURL(url)
}

func (s *supervisor) url() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.ready.URL
}

func (s *supervisor) stop() {
	s.mu.Lock()
	s.closing = true
	cp := s.cur
	s.mu.Unlock()
	if cp != nil {
		cp.shutdown(45 * time.Second)
	}
}
