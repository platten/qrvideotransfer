//go:build windows

package video

import (
	"os"
	"reflect"
	"runtime"

	"golang.org/x/sys/windows"
)

// killOnClose puts the worker in a Job Object that kills it when the job's last handle closes,
// which happens when this process exits for any reason. The worker's own exit on a closed pipe is
// the baseline; this adds the operating system's guarantee. Failures are ignored.
func killOnClose(p *os.Process) func() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	// reflect gives the struct's address without importing unsafe (SECURITY.md section 9.6).
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		reflect.ValueOf(&info).Pointer(), uint32(reflect.TypeOf(info).Size())) //nolint:gosec // a small struct
	runtime.KeepAlive(&info)
	if err == nil {
		if h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid)); err == nil { //nolint:gosec // a process id
			_ = windows.AssignProcessToJobObject(job, h)
			_ = windows.CloseHandle(h)
		}
	}
	return func() { _ = windows.CloseHandle(job) }
}
