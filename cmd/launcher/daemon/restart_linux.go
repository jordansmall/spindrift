//go:build linux && (amd64 || arm64)

package main

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// inPlaceRestartSupported gates --restart-in-place. The exec path drives the
// stop signals through raw rt_sigaction and Pipe2, which only exist here;
// restart_other.go refuses the flag everywhere else.
// A var so a test can exercise the refusal path.
var inPlaceRestartSupported = true

const (
	// sigsetBytes is the kernel's sigset size for rt_sigaction (64 signals).
	sigsetBytes = 8
	sigDefault  = 0 // SIG_DFL as a kernel handler value
	sigIgnore   = 1 // SIG_IGN
)

// kernelSigaction is the kernel's struct sigaction on linux amd64/arm64, the
// layout Go's runtime uses for its own rt_sigaction calls.
type kernelSigaction struct {
	handler, flags, restorer uintptr
	mask                     uint64
}

// sigaction installs act for sig (nil to only read) and returns the previous
// kernel action.
func sigaction(sig syscall.Signal, act *kernelSigaction) (old kernelSigaction, err error) {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig),
		uintptr(unsafe.Pointer(act)), uintptr(unsafe.Pointer(&old)), sigsetBytes, 0, 0)
	if errno != 0 {
		return kernelSigaction{}, errno
	}
	return old, nil
}

// sigHUPIgnored reports that SIGHUP is already ignored (nohup), which the
// successor must keep rather than restore to the default.
func sigHUPIgnored() (bool, error) {
	cur, err := sigaction(syscall.SIGHUP, nil)
	if err != nil {
		return false, fmt.Errorf("read SIGHUP action: %w", err)
	}
	return cur.handler == sigIgnore, nil
}

// ignoreStopSignals sets the stop signals to SIG_IGN and returns a func putting
// every saved action back exactly. On error nothing stays changed.
func ignoreStopSignals() (restore func(), err error) {
	saved := make(map[syscall.Signal]kernelSigaction, len(stopSignals))
	restore = func() {
		for sig, old := range saved {
			_, _ = sigaction(sig, &old)
		}
	}
	for _, sig := range stopSignals {
		ign := kernelSigaction{handler: sigIgnore}
		old, err := sigaction(sig, &ign)
		if err != nil {
			restore()
			return nil, fmt.Errorf("ignore %v: %w", sig, err)
		}
		saved[sig] = old
	}
	return restore, nil
}

// restoreSIGHUP gives a successor's SIGHUP its default action back when the
// exec left it ignored by ignoreStopSignals (hupWasIgnored false). Only an
// ignored SIGHUP is touched, so a Go handler installed since is left alone.
func restoreSIGHUP(hupWasIgnored bool) error {
	if hupWasIgnored {
		return nil
	}
	cur, err := sigaction(syscall.SIGHUP, nil)
	if err != nil || cur.handler != sigIgnore {
		return err
	}
	_, err = sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigDefault})
	return err
}

// handoffDocPipe writes the encoded document into a fresh pipe and returns the
// read end, inheritable. The write is non-blocking, so a document past the
// pipe's capacity is refused instead of deadlocking before the exec.
func handoffDocPipe(doc []byte) (int, error) {
	var p [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe2(p[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK)
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, fmt.Errorf("handoff pipe: %w", err)
	}
	defer syscall.Close(p[0])
	n, err := syscall.Write(p[1], doc)
	_ = syscall.Close(p[1])
	if err != nil && !errors.Is(err, syscall.EAGAIN) {
		return -1, fmt.Errorf("write handoff document: %w", err)
	}
	if n < len(doc) {
		return -1, fmt.Errorf("handoff document (%d bytes) exceeds the pipe's capacity", len(doc))
	}
	return dupInheritable(p[0])
}
