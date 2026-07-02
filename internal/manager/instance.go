package manager

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// State is an instance lifecycle state.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateStopping State = "stopping"
)

// Instance is a single running (or transitioning) llama-server process. Model,
// Port and StartedAt are set once at creation and read-only afterwards; state is
// atomic so the UI can read it without contending on the manager's swap lock.
type Instance struct {
	Model     string
	Port      int
	StartedAt time.Time

	state     atomic.Value // State
	cmd       *exec.Cmd
	logs      *ringBuffer
	idleTimer *time.Timer
	exited    chan struct{} // closed by reap when the process exits
}

// State returns the instance's current lifecycle state (lock-free).
func (i *Instance) State() State {
	if v := i.state.Load(); v != nil {
		return v.(State)
	}
	return StateStopped
}

// setState atomically updates the lifecycle state.
func (i *Instance) setState(s State) { i.state.Store(s) }

// freePort asks the OS for an unused TCP port on localhost.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// signalGroup sends sig to the instance's process group so child processes
// (e.g. a spawned drafter) die too.
func (i *Instance) signalGroup(sig syscall.Signal) error {
	if i.cmd == nil || i.cmd.Process == nil {
		return nil
	}
	// Negative pid targets the whole process group (Setpgid was set on start).
	return syscall.Kill(-i.cmd.Process.Pid, sig)
}

// procAttr runs the child in its own process group.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// ringBuffer keeps the last maxBytes of process output for the UI.
type ringBuffer struct {
	mu       sync.Mutex
	buf      []byte
	maxBytes int
}

func newRingBuffer(maxBytes int) *ringBuffer {
	return &ringBuffer{maxBytes: maxBytes}
}

// Write appends p, trimming from the front once the cap is exceeded.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.maxBytes {
		r.buf = r.buf[len(r.buf)-r.maxBytes:]
	}
	return len(p), nil
}

// String returns the buffered output.
func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// BaseURL is the http address of the instance.
func (i *Instance) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", i.Port)
}

// prefixWriter prepends prefix to every line written to w. Partial lines are
// buffered until a newline arrives, so prefixes never land mid-line.
type prefixWriter struct {
	w      io.Writer
	prefix []byte
	mu     sync.Mutex
	buf    []byte
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: []byte(prefix)}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, 0, len(p.prefix)+i+1)
		line = append(line, p.prefix...)
		line = append(line, p.buf[:i+1]...)
		if _, err := p.w.Write(line); err != nil {
			return len(b), err
		}
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}
