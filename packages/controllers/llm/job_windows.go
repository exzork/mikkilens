//go:build windows

package llm

import (
	"log/slog"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A model server must not outlive MikkiLens.
//
// The server holds three gigabytes of model in memory. If MikkiLens is closed
// abruptly -- force quit, a crash, the machine's task manager -- the ordinary
// shutdown never runs and that child is left behind, holding its memory and
// its port until the machine is restarted. Worse, the next start spawns
// another, so the machine quietly loses several gigabytes per launch to
// processes nobody can see the purpose of.
//
// A Windows job object with kill-on-close makes that impossible: the child is
// attached to the job, and when the last handle to the job closes -- which
// happens however this process dies -- Windows terminates everything in it.

var (
	jobOnce   sync.Once
	jobHandle windows.Handle
)

// adopt ties a child process to this one's lifetime.
//
// Failing is not fatal: the child still runs and still gets stopped normally.
// What is lost is only the guarantee about an abrupt exit, which is worth a
// log line rather than refusing to start the model at all.
func adopt(process *os.Process) {
	if process == nil {
		return
	}

	jobOnce.Do(func() {
		handle, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			slog.Warn("could not create the process job", "error", err)
			return
		}

		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags =
			windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

		if _, err := windows.SetInformationJobObject(
			handle,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)),
			uint32(unsafe.Sizeof(limits)),
		); err != nil {
			slog.Warn("could not configure the process job", "error", err)
			windows.CloseHandle(handle)
			return
		}
		jobHandle = handle
	})

	if jobHandle == 0 {
		return
	}

	// The handle Go holds is not one we can hand to the job, so the process is
	// opened again with just the rights the job needs.
	target, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(process.Pid))
	if err != nil {
		slog.Warn("could not open the model server to adopt it", "error", err)
		return
	}
	defer windows.CloseHandle(target)

	if err := windows.AssignProcessToJobObject(jobHandle, target); err != nil {
		slog.Warn("could not adopt the model server", "error", err)
	}
}
