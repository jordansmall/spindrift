package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// superviseChild runs argv as the only child of this process, forwards
// termination signals to it, and reaps every orphan the kernel reparents here.
// In the podman Box box is PID 1, which adopts orphans; reaping them from the
// same process that runs os/exec children would race those Waits for their
// exit statuses, so the real work runs in a child and this process only reaps.
// It returns the child's exit code, 128+signal when the child was killed.
func superviseChild(path string, argv []string, stderr io.Writer) (int, error) {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	// No Setpgid: on a tty Ctrl-C reaches the worker twice (shared foreground
	// group, then our forward), which is harmless; Setpgid would background it
	// and SIGTTIN its tty reads.
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()},
	})
	if err != nil {
		return 0, err
	}

	go func() {
		for s := range sigs {
			if sig, ok := s.(syscall.Signal); ok {
				_ = syscall.Kill(pid, sig)
			}
		}
	}()

	for {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			fmt.Fprintln(stderr, "box: init: wait:", err)
			// Not an error return: the caller would fall back to running in
			// process beside a possibly live worker. Exiting kills it with the pidns.
			return 1, nil
		}
		if wpid != pid {
			continue
		}
		if ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ws.ExitStatus(), nil
	}
}

// runAsInit is main's PID 1 path: re-run this binary under superviseChild. Any
// other pid reports false untouched. A failure to start the child also reports
// false so the caller runs in-process; a broken reaper must never cost the
// run. pid and exe are parameters so tests can drive the real path.
func runAsInit(pid int, exe func() (string, error), args []string, stderr io.Writer) (int, bool) {
	if pid != 1 {
		return 0, false
	}
	self, err := exe()
	if err == nil {
		var rc int
		if rc, err = superviseChild(self, append([]string{self}, args...), stderr); err == nil {
			return rc, true
		}
	}
	fmt.Fprintln(stderr, "box: init:", err)
	return 0, false
}
