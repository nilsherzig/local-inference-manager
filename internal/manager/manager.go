// Package manager owns the llama-server instance lifecycle. Exactly one instance
// runs at a time (pure swap). Ensure holds a single mutex across start + health
// check, which naturally implements the cold-start hold: requests for a model
// that is still starting block until it is Ready.
package manager

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nilsherzig/local-inference-manager/internal/config"
	"github.com/nilsherzig/local-inference-manager/internal/events"
)

// Publisher receives lifecycle and queue updates for the UI.
type Publisher interface {
	Publish(topic string, data any)
}

// Manager coordinates the single active instance.
type Manager struct {
	cfg    *config.Config
	bus    Publisher
	client *http.Client

	// swapMu serializes swaps (= cold-start hold): it is held across start +
	// health poll, so concurrent Ensure calls block until the instance is Ready.
	// It is NEVER held for reads, so Snapshot never blocks the web UI while a
	// model is starting.
	swapMu  sync.Mutex
	current atomic.Pointer[Instance]

	queueDepth atomic.Int64

	// streamLogs mirrors instance stdout/stderr to os.Stdout/os.Stderr.
	streamLogs bool
}

// Option configures a Manager.
type Option func(*Manager)

// WithLogStreaming mirrors each instance's stdout/stderr to the manager's own
// stdout/stderr, prefixing every line with the model name.
func WithLogStreaming() Option {
	return func(m *Manager) { m.streamLogs = true }
}

// Snapshot is a UI-friendly view of the current state.
type Snapshot struct {
	Running    bool
	Model      string
	Port       int
	State      State
	StartedAt  time.Time
	Uptime     time.Duration
	Logs       string
	QueueDepth int64
}

// New creates a Manager. bus may be a no-op publisher in tests.
func New(cfg *config.Config, bus Publisher, opts ...Option) *Manager {
	m := &Manager{
		cfg:    cfg,
		bus:    bus,
		client: &http.Client{Timeout: 5 * time.Second},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Ensure guarantees the named (canonical) model is running and Ready, swapping
// out any other instance first. The returned instance is safe to proxy to.
func (m *Manager) Ensure(canonical string) (*Instance, error) {
	m.queueDepth.Add(1)
	m.publishQueue()
	defer func() {
		m.queueDepth.Add(-1)
		m.publishQueue()
	}()

	m.swapMu.Lock()
	defer m.swapMu.Unlock()

	if cur := m.current.Load(); cur != nil && cur.Model == canonical && cur.State() == StateReady {
		log.Printf("manager: reusing running instance %q", canonical)
		m.resetIdle(cur)
		return cur, nil
	}

	if cur := m.current.Load(); cur != nil {
		log.Printf("manager: swapping %q out to start %q", cur.Model, canonical)
		m.stopCurrentLocked()
	}

	inst, err := m.startLocked(canonical)
	if err != nil {
		return nil, err
	}
	m.resetIdle(inst)
	return inst, nil
}

// startLocked launches a new process and blocks until it is healthy. swapMu held.
func (m *Manager) startLocked(canonical string) (*Instance, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate port: %w", err)
	}
	args, err := m.cfg.Args(canonical, strconv.Itoa(port))
	if err != nil {
		return nil, err
	}

	logs := newRingBuffer(64 * 1024)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = procAttr()
	cmd.Stdout = logs
	cmd.Stderr = logs
	if m.streamLogs {
		prefix := "[" + canonical + "] "
		cmd.Stdout = io.MultiWriter(logs, newPrefixWriter(os.Stdout, prefix))
		cmd.Stderr = io.MultiWriter(logs, newPrefixWriter(os.Stderr, prefix))
	}

	inst := &Instance{
		Model:     canonical,
		Port:      port,
		StartedAt: time.Now(),
		cmd:       cmd,
		logs:      logs,
		exited:    make(chan struct{}),
	}
	inst.setState(StateStarting)
	m.current.Store(inst) // publish "starting" state to the UI while we wait
	m.publishInstance()
	log.Printf("manager: starting %q on port %d: %s", canonical, port, strings.Join(args, " "))

	if err := cmd.Start(); err != nil {
		m.current.Store(nil)
		log.Printf("manager: failed to start %q: %v", canonical, err)
		return nil, fmt.Errorf("start %q: %w", canonical, err)
	}

	go m.reap(inst)

	started := time.Now()
	if err := m.waitHealthy(inst); err != nil {
		log.Printf("manager: %q failed health check: %v", canonical, err)
		m.terminate(inst)
		m.current.Store(nil)
		m.publishInstance()
		return nil, fmt.Errorf("model %q did not become healthy: %w", canonical, err)
	}
	inst.setState(StateReady)
	m.publishInstance()
	log.Printf("manager: %q ready on port %d after %s", canonical, port, time.Since(started).Round(time.Millisecond))
	return inst, nil
}

// reap waits for the process to exit and handles unexpected crashes.
func (m *Manager) reap(inst *Instance) {
	_ = inst.cmd.Wait()
	close(inst.exited)

	m.swapMu.Lock()
	defer m.swapMu.Unlock()
	// Only react to unexpected exits; a planned stop already set Stopping.
	if m.current.Load() == inst && inst.State() == StateReady {
		log.Printf("manager: %q exited unexpectedly", inst.Model)
		inst.setState(StateStopped)
		if inst.idleTimer != nil {
			inst.idleTimer.Stop()
		}
		m.current.Store(nil)
		m.publishInstance()
	}
}

// waitHealthy polls /health until 200 or the configured timeout. swapMu held.
func (m *Manager) waitHealthy(inst *Instance) error {
	deadline := time.Now().Add(time.Duration(m.cfg.Manager.HealthTimeout) * time.Second)
	url := inst.BaseURL() + "/health"
	log.Printf("manager: waiting for %q to become healthy (timeout %ds)", inst.Model, m.cfg.Manager.HealthTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-inst.exited:
			return fmt.Errorf("process exited during startup")
		default:
		}
		resp, err := m.client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("health timeout after %ds", m.cfg.Manager.HealthTimeout)
}

// stopCurrentLocked terminates and clears the current instance. swapMu held.
func (m *Manager) stopCurrentLocked() {
	cur := m.current.Load()
	if cur == nil {
		return
	}
	m.terminate(cur)
	m.current.Store(nil)
	m.publishInstance()
}

// terminate signals SIGTERM, then SIGKILL after a grace period. swapMu held.
func (m *Manager) terminate(inst *Instance) {
	log.Printf("manager: stopping %q (SIGTERM)", inst.Model)
	inst.setState(StateStopping)
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
	}
	_ = inst.signalGroup(syscall.SIGTERM)
	select {
	case <-inst.exited:
	case <-time.After(10 * time.Second):
		log.Printf("manager: %q did not exit within 10s, sending SIGKILL", inst.Model)
		_ = inst.signalGroup(syscall.SIGKILL)
		<-inst.exited
	}
	inst.setState(StateStopped)
	log.Printf("manager: %q stopped", inst.Model)
}

// resetIdle (re)arms the idle timer for inst. swapMu held.
func (m *Manager) resetIdle(inst *Instance) {
	if inst == nil {
		return
	}
	ttl := time.Duration(m.cfg.TTL(inst.Model)) * time.Second
	if inst.idleTimer != nil {
		inst.idleTimer.Reset(ttl)
		return
	}
	target := inst
	inst.idleTimer = time.AfterFunc(ttl, func() {
		m.swapMu.Lock()
		defer m.swapMu.Unlock()
		if m.current.Load() == target && target.State() == StateReady {
			log.Printf("manager: stopping %q after %s idle", target.Model, ttl)
			m.stopCurrentLocked()
		}
	})
}

// Touch resets the idle timer; call it at the start of each proxied request.
func (m *Manager) Touch() {
	m.swapMu.Lock()
	defer m.swapMu.Unlock()
	if cur := m.current.Load(); cur != nil {
		m.resetIdle(cur)
	}
}

// Stop terminates the current instance (UI stop button / shutdown).
func (m *Manager) Stop() {
	m.swapMu.Lock()
	defer m.swapMu.Unlock()
	if m.current.Load() == nil {
		log.Println("manager: stop requested, no instance running")
		return
	}
	m.stopCurrentLocked()
}

// Snapshot returns the current state for the UI. It is lock-free (reads an
// atomic pointer + atomic state), so it never blocks while a model is starting.
func (m *Manager) Snapshot() Snapshot {
	s := Snapshot{QueueDepth: m.queueDepth.Load()}
	if cur := m.current.Load(); cur != nil {
		s.Running = true
		s.Model = cur.Model
		s.Port = cur.Port
		s.State = cur.State()
		s.StartedAt = cur.StartedAt
		s.Uptime = time.Since(cur.StartedAt)
		s.Logs = cur.logs.String()
	}
	return s
}

func (m *Manager) publishInstance() {
	if m.bus != nil {
		m.bus.Publish(events.TopicInstances, m.Snapshot())
	}
}

func (m *Manager) publishQueue() {
	if m.bus != nil {
		m.bus.Publish(events.TopicQueue, m.queueDepth.Load())
	}
}
