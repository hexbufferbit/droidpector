package vm

import (
	"fmt"
	"strings"
)

// StartError is a user-facing explanation of a VM start failure.
type StartError struct {
	Title   string   `json:"title"`
	Causes  []string `json:"causes"`
	Details string   `json:"details"` // diagnostic output (QEMU stderr, exit status)
	Code    string   `json:"code"`    // stable identifier for the UI and tests
}

func (e *StartError) Error() string {
	var sb strings.Builder
	sb.WriteString(e.Title)
	if len(e.Causes) > 0 {
		sb.WriteString("\n\nPossible causes:")
		for _, c := range e.Causes {
			sb.WriteString("\n• ")
			sb.WriteString(c)
		}
	}
	if e.Details != "" {
		sb.WriteString("\n\nSee Details for diagnostic information.")
	}
	return sb.String()
}

// Error codes.
const (
	CodeAccelUnavailable = "accel_unavailable"
	CodeMissingRuntime   = "missing_runtime"
	CodeOutOfMemory      = "out_of_memory"
	CodeDiskError        = "disk_error"
	CodeQEMUNotFound     = "qemu_not_found"
	CodeQEMUFailed       = "qemu_failed"
	CodeBootTimeout      = "boot_timeout"
	CodeCPUUnsupported   = "cpu_unsupported"
)

// classifyQEMUFailure turns QEMU's stderr into an actionable StartError.
func classifyQEMUFailure(stderr string, exitErr error, accel string) *StartError {
	s := strings.ToLower(stderr)
	details := strings.TrimSpace(stderr)
	if exitErr != nil {
		details = strings.TrimSpace(details + "\n" + exitErr.Error())
	}
	switch {
	case strings.Contains(s, "whpx") && (strings.Contains(s, "no accelerator") || strings.Contains(s, "failed") || strings.Contains(s, "not supported")):
		return &StartError{Code: CodeAccelUnavailable, Title: "Hardware acceleration (Windows Hypervisor Platform) is not available.",
			Causes: []string{
				"The \"Windows Hypervisor Platform\" feature is not enabled (Windows Features → Windows Hypervisor Platform, then restart).",
				"Hardware virtualization (Intel VT-x / AMD-V) is disabled in the BIOS/UEFI settings.",
				"Another virtualization product is holding the hypervisor exclusively.",
			}, Details: details}
	case strings.Contains(s, "kvm") && (strings.Contains(s, "permission denied") || strings.Contains(s, "no such file") || strings.Contains(s, "failed to initialize kvm")):
		return &StartError{Code: CodeAccelUnavailable, Title: "Hardware acceleration (KVM) is not available.",
			Causes:  []string{"/dev/kvm is missing or not accessible to this user (add the user to the kvm group).", "Virtualization is disabled in the firmware."},
			Details: details}
	case strings.Contains(s, "hvf") && strings.Contains(s, "error"):
		return &StartError{Code: CodeAccelUnavailable, Title: "Hardware acceleration (Hypervisor.framework) is not available.", Details: details}
	case strings.Contains(s, "cannot set up guest memory") || strings.Contains(s, "cannot allocate memory") || strings.Contains(s, "out of memory"):
		return &StartError{Code: CodeOutOfMemory, Title: "Android sandbox could not start: not enough memory.",
			Causes: []string{"Close other applications or lower the sandbox memory in Settings."}, Details: details}
	case strings.Contains(s, "could not open") || strings.Contains(s, "no such file") || strings.Contains(s, "failed to get \"write\" lock") || strings.Contains(s, "image is not in qcow2"):
		causes := []string{"Required runtime files are missing or damaged (reinstall droidpector).", "The sandbox disk is in use by another droidpector instance."}
		if strings.Contains(s, "lock") {
			causes = []string{"Another droidpector instance is already running this sandbox."}
		}
		return &StartError{Code: CodeDiskError, Title: "Android sandbox could not start: a virtual disk could not be opened.", Causes: causes, Details: details}
	case strings.Contains(s, "sse4") || strings.Contains(s, "cpu does not support"):
		return &StartError{Code: CodeCPUUnsupported, Title: "This computer's processor lacks features Android requires (SSE4.2).", Details: details}
	}
	return &StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start.",
		Causes: []string{
			"Hardware virtualization is disabled.",
			"Required runtime files are missing.",
			"Another virtualization backend is using the resource.",
		}, Details: details + fmt.Sprintf("\naccelerator: %s", accel)}
}
