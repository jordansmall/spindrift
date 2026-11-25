//go:build integration

package escape

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

func TestEscapeControlReachesWhatTheSandboxMustNot(t *testing.T) {
	host, skip, err := HostDir()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	Control(t, host)
}

// bareStarter starts each Connector sandbox as an unsandboxed connector-host.
type bareStarter struct {
	host string
	mu   sync.Mutex
	cmds map[string]*exec.Cmd
}

func (s *bareStarter) StartConnectorSandbox(name string) (runner.ConnectorSandbox, error) {
	cmd := exec.Command(filepath.Join(s.host, "bin", "connector-host"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return runner.ConnectorSandbox{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmds == nil {
		s.cmds = map[string]*exec.Cmd{}
	}
	s.cmds[name] = cmd
	return runner.ConnectorSandbox{Stdin: stdin, Stdout: stdout, Wait: cmd.Wait, Stderr: stderr.String}, nil
}

func (s *bareStarter) Kill(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := s.cmds[name]
	if cmd == nil {
		return fmt.Errorf("no sandbox %q", name)
	}
	// A host whose pipe already failed has exited; the sandbox must still see
	// that stop succeed, or it never marks the generation stopped.
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// lockedBuffer keeps reads of stderr synchronized with exec's copy goroutine,
// which can still be writing when the Launcher asks for the text.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The Launcher's refusals do not depend on the sandbox, so the battery's
// assertions hold over unsandboxed hosts too, which proves the battery itself
// where no real sandbox runs.
func TestImpersonationIsRefusedOverUnsandboxedHosts(t *testing.T) {
	host, skip, err := HostDir()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	Impersonation(t, &bareStarter{host: host})
}
