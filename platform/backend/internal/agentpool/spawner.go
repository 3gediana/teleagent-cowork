package agentpool

// Spawner abstracts "start a client subprocess". Production uses
// `execSpawner` (os/exec + waiting on /global/health); tests plug in
// FakeSpawner to avoid actually booting opencode.
//
// Separating this from Manager means the pool's lifecycle logic
// (port allocation, DB bookkeeping, crash detection) is testable in
// isolation without a real subprocess.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SpawnerRequest is what the Manager hands a Spawner when it wants a
// subprocess to come up. Env is merged with the caller's os.Environ()
// so the child inherits standard vars (PATH, HOME) without the pool
// having to enumerate them.
type SpawnerRequest struct {
	WorkingDir string
	Port       int
	Env        map[string]string
	Command    string   // empty = spawner picks default
	Args       []string // appended to Command
}

// SpawnerHandle is the live subprocess. Manager holds it, queries
// PID/Wait, and calls Terminate on planned shutdown.
type SpawnerHandle interface {
	// PID identifies the subprocess to the OS. -1 for fakes.
	PID() int

	// WaitHealthy blocks until the subprocess's health endpoint is
	// responsive, or timeout elapses. Returns false on timeout.
	// Safe to call exactly once per spawn; subsequent calls return
	// whatever the first call returned.
	WaitHealthy(ctx context.Context, timeout time.Duration) bool

	// Wait returns a channel that closes with the exit code once
	// the subprocess stops (clean or otherwise).
	Wait() <-chan int

	// Terminate is graceful-then-force: TERM, wait up to `grace`,
	// KILL if still alive. Returns once the subprocess is gone.
	Terminate(grace time.Duration)
}

// Spawner creates subprocesses on demand.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnerRequest) (SpawnerHandle, error)
}

// ----- execSpawner -------------------------------------------------------

// execSpawner is the real implementation — shells out to `opencode
// serve` (or the operator's override from ManagerConfig.Command).
type execSpawner struct{}

func (s *execSpawner) Spawn(ctx context.Context, req SpawnerRequest) (SpawnerHandle, error) {
	if err := os.MkdirAll(req.WorkingDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workdir: %w", err)
	}

	// Default command: "opencode serve --port <N>". If the operator
	// pins a custom command, we append --port only when it's absent
	// from the args — gives escape hatches for non-opencode harnesses.
	cmdPath := req.Command
	args := req.Args
	if cmdPath == "" {
		cmdPath = defaultOpencodeCommand()
		args = []string{"serve", "--port", fmt.Sprintf("%d", req.Port)}
	} else {
		// Only auto-add --port if the operator didn't set one.
		hasPort := false
		for _, a := range args {
			if a == "--port" {
				hasPort = true
				break
			}
		}
		if !hasPort {
			args = append(args, "--port", fmt.Sprintf("%d", req.Port))
		}
	}

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Dir = req.WorkingDir

	// Merge env so child inherits PATH etc. but gets our overrides.
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// Pipe stdout/stderr to server log (prefixed) so operators can
	// see the client's output in the platform's journalctl/stdout.
	logPrefix := fmt.Sprintf("[Pool:%s]", filepath.Base(req.WorkingDir))
	cmd.Stdout = prefixWriter{prefix: logPrefix, target: os.Stdout}
	cmd.Stderr = prefixWriter{prefix: logPrefix + " stderr", target: os.Stderr}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec start: %w", err)
	}

	h := &execHandle{
		cmd:     cmd,
		port:    req.Port,
		done:    make(chan int, 1),
		once:    sync.Once{},
	}
	go h.reap()
	return h, nil
}

type execHandle struct {
	cmd     *exec.Cmd
	port    int
	done    chan int
	once    sync.Once
	exitVal int
}

func (h *execHandle) PID() int {
	if h.cmd == nil || h.cmd.Process == nil {
		return -1
	}
	return h.cmd.Process.Pid
}

func (h *execHandle) WaitHealthy(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/global/health", h.port)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case code := <-h.done:
			// Subprocess exited before health went green. Stuff the
			// exit code back so Wait() still returns it.
			h.done <- code
			return false
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func (h *execHandle) Wait() <-chan int { return h.done }

func (h *execHandle) reap() {
	err := h.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	h.exitVal = code
	// Non-blocking send so reap doesn't hang if no one's listening.
	select {
	case h.done <- code:
	default:
	}
}

func (h *execHandle) Terminate(grace time.Duration) {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	h.once.Do(func() {
		// Prefer TERM; fall back to KILL. Windows doesn't honour
		// Unix signals the same way — fall straight to Kill there.
		if runtimeSupportsTerm() {
			_ = h.cmd.Process.Signal(syscall.SIGTERM)
		} else {
			_ = h.cmd.Process.Kill()
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-h.done:
			return
		case <-timer.C:
			log.Printf("[Pool] PID %d didn't exit in %s; KILL", h.PID(), grace)
			_ = h.cmd.Process.Kill()
		}
	})
}

// runtimeSupportsTerm: Windows has no SIGTERM — short-circuit to KILL
// there. Uses build-time GOOS discovery via os.Getenv fallback is
// wrong; we read from runtime.GOOS-equivalent via a small helper.
// The helper is here (not a separate file) so this whole package
// stays importable without cgo / goos tags.
//
//go:noinline
func runtimeSupportsTerm() bool {
	// Cheap check via env var first — GOOS isn't set at runtime
	// but os.PathSeparator behaves OS-specifically. On Windows
	// `os.PathSeparator` is `\`. No imports added.
	return os.PathSeparator == '/'
}

// prefixWriter labels each line of child output with the instance
// id so operators can skim mixed-instance logs.
type prefixWriter struct {
	prefix string
	target *os.File
}

func (p prefixWriter) Write(b []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for _, l := range lines {
		if l == "" {
			continue
		}
		fmt.Fprintf(p.target, "%s %s\n", p.prefix, l)
	}
	return len(b), nil
}

// ----- virtualHandle (no subprocess) ------------------------------------

// virtualHandle is used when pool agents share the operator's local
// opencode serve (port 4096) instead of spawning their own subprocess.
// It always reports healthy immediately and never exits on its own.
type virtualHandle struct {
	port int
}

func (v *virtualHandle) PID() int { return -1 }

func (v *virtualHandle) WaitHealthy(_ context.Context, _ time.Duration) bool {
	// Check that the shared opencode serve is actually running.
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/global/health", v.port)
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("[Pool] virtual agent: opencode serve on port %d not reachable: %v", v.port, err)
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == 200
}

func (v *virtualHandle) Wait() <-chan int {
	ch := make(chan int, 1)
	// Virtual agents never exit on their own; the channel stays
	// open until Terminate is called.
	return ch
}

func (v *virtualHandle) Terminate(_ time.Duration) {
	// Nothing to terminate — no subprocess exists.
}

// defaultOpencodeCommand is what we exec when no override is
// configured. Resolvable through PATH, matching how the user already
// runs opencode from their shell. Operators with a custom binary
// location should set ManagerConfig.Command explicitly.
func defaultOpencodeCommand() string {
	return "opencode"
}

// ----- FakeSpawner (tests) -----------------------------------------------

// FakeSpawner returns a handle whose WaitHealthy returns true after
// HealthDelay, whose Wait blocks until ExitAfter elapses or Terminate
// is called. Tests use it to drive Manager without a real
// subprocess.
type FakeSpawner struct {
	HealthDelay time.Duration // time to "come up"
	ExitAfter   time.Duration // if >0, auto-exit after this; else run forever
	PIDBase     int           // starting PID for handles
	mu          sync.Mutex
	next        int
}

func (f *FakeSpawner) Spawn(ctx context.Context, req SpawnerRequest) (SpawnerHandle, error) {
	f.mu.Lock()
	f.next++
	pid := f.PIDBase + f.next
	f.mu.Unlock()
	h := &fakeHandle{
		pid:       pid,
		port:      req.Port,
		healthDel: f.HealthDelay,
		exitAfter: f.ExitAfter,
		done:      make(chan int, 1),
		stop:      make(chan struct{}),
	}
	if h.exitAfter > 0 {
		go func() {
			select {
			case <-time.After(h.exitAfter):
				select { case h.done <- 0: default: }
			case <-h.stop:
				select { case h.done <- 0: default: }
			}
		}()
	}
	return h, nil
}

type fakeHandle struct {
	pid       int
	port      int
	healthDel time.Duration
	exitAfter time.Duration
	done      chan int
	stop      chan struct{}
	once      sync.Once
}

func (h *fakeHandle) PID() int { return h.pid }

func (h *fakeHandle) WaitHealthy(ctx context.Context, timeout time.Duration) bool {
	if h.healthDel > timeout {
		return false
	}
	select {
	case <-time.After(h.healthDel):
		return true
	case <-ctx.Done():
		return false
	}
}

func (h *fakeHandle) Wait() <-chan int { return h.done }

func (h *fakeHandle) Terminate(grace time.Duration) {
	h.once.Do(func() {
		close(h.stop)
		select {
		case h.done <- 0:
		default:
		}
	})
}
