package sandbox

import (
	"fmt"
	"slices"
	"sync"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
)

// lazyStarter is a Sandbox's Starter that builds its runner on the first
// start, so a process that loads no Connector builds none.
type lazyStarter struct {
	build func() (runner.Runner, error)
	once  sync.Once
	r     runner.Runner
	err   error
}

func (l *lazyStarter) get() (runner.Runner, error) {
	l.once.Do(func() { l.r, l.err = l.build() })
	return l.r, l.err
}

func (l *lazyStarter) StartConnectorSandbox(name string) (runner.ConnectorSandbox, error) {
	r, err := l.get()
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	return r.StartConnectorSandbox(name)
}

func (l *lazyStarter) Kill(name string) error {
	r, err := l.get()
	if err != nil {
		return err
	}
	return r.Kill(name)
}

// ForKind returns a Pool that starts its sandboxes through the runner family
// kind names, built on the first load. It is the one way a host process outside
// a Box (the Launcher, the daemon) arms the sandboxes its Connector loads go
// through; the caller supplies the config, artifacts and knob lookup its own
// runner would use.
func ForKind(kind string, rc runner.Config, art inputdoc.Artifacts, l runner.KnobLookup, pwd string) *Pool {
	return NewPool(&lazyStarter{build: func() (runner.Runner, error) {
		return runner.New(kind, rc, art, l, pwd)
	}}, runnerLabel(kind, rc.Runtime))
}

// runnerLabel names the runner a Connector sandbox starts through in a start
// error: the family, plus runtime only when that family serves it, so bwrap
// beside RUNTIME=podman is not named podman.
func runnerLabel(kind, runtime string) string {
	family, err := runner.DescriptorFor(kind)
	if err != nil {
		return kind
	}
	if slices.Contains(family.Runtimes, runtime) && runtime != family.Name {
		return fmt.Sprintf("%s (%s)", family.Name, runtime)
	}
	return family.Name
}
