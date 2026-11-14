//go:build windows

package video

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

// startWorker starts the worker, with job in a Job Object that kills it when the job's last handle
// closes, which happens when this process exits for any reason; release closes that handle. The
// worker's own exit on a closed pipe is the baseline; the job adds the operating system's guarantee.
// When no job can be made the worker starts without one.
func startWorker(cmd *exec.Cmd, job bool) (release func(), err error) {
	if !job {
		return func() {}, cmd.Start()
	}
	h, err := startWorkerJob(cmd)
	if err != nil || h == 0 {
		return func() {}, err
	}
	return func() { _ = windows.CloseHandle(h) }, nil
}

// startWorkerJob makes a kill-on-close Job Object and starts cmd suspended, assigns it to the job and
// only then resumes it, so the worker never runs outside the job. When no job can be made or the
// worker cannot be assigned to it, the worker runs without one and the handle is 0. When it cannot be
// resumed it is killed.
func startWorkerJob(cmd *exec.Cmd) (windows.Handle, error) {
	job, err := newKillJob()
	if err != nil {
		return 0, cmd.Start()
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	pid := uint32(cmd.Process.Pid)  //nolint:gosec // a process id
	if assignJob(job, pid) != nil { // the job is an addition to the pipe exit, not a requirement
		_ = windows.CloseHandle(job)
		job = 0
	}
	if err := resume(pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		return 0, fmt.Errorf("resuming the worker: %w", err)
	}
	return job, nil
}

// newKillJob creates a Job Object that kills its processes when its last handle closes.
func newKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		addr(&info), uint32(reflect.TypeOf(info).Size())) //nolint:gosec // a small struct
	runtime.KeepAlive(&info)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignJob(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.AssignProcessToJobObject(job, h)
}

// resume resumes the threads of a process created suspended (its main thread only).
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	te := windows.ThreadEntry32{Size: uint32(reflect.TypeOf(windows.ThreadEntry32{}).Size())} //nolint:gosec // a small struct
	n := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(h)
		_ = windows.CloseHandle(h)
		if err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errors.New("no thread to resume")
	}
	return nil
}

// addr is p's address for a system call. It is taken with reflect rather than unsafe: the one
// reviewed exception to the import rule's intent (docs/SECURITY.md section 9.6), which a test keeps
// to this file. p is heap-allocated (reflect makes it escape), so it does not move; the caller keeps
// it alive across the call with runtime.KeepAlive.
func addr[T any](p *T) uintptr { return reflect.ValueOf(p).Pointer() }
