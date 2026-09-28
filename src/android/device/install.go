package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// InstallOptions tunes package installation.
type InstallOptions struct {
	// ABI forces the primary ABI (pm install --abi), e.g. "arm64-v8a".
	ABI string
	// AllowDowngrade passes -d.
	AllowDowngrade bool
	// BypassLowTargetSDK passes --bypass-low-target-sdk-block (Android 14+).
	// Install retries with it automatically on INSTALL_FAILED_DEPRECATED_SDK_VERSION.
	BypassLowTargetSDK bool
}

func (o InstallOptions) flags() string {
	f := "-r -t -g"
	if o.AllowDowngrade {
		f += " -d"
	}
	if o.ABI != "" {
		f += " --abi " + quote(o.ABI)
	}
	if o.BypassLowTargetSDK {
		f += " --bypass-low-target-sdk-block"
	}
	return f
}

// Install failure codes with dedicated handling.
const (
	CodeDeprecatedSDK = "INSTALL_FAILED_DEPRECATED_SDK_VERSION"
	CodeUnknown       = "INSTALL_FAILED_UNKNOWN"
)

// installMessages maps PackageManager failure codes to user-facing text.
var installMessages = map[string]string{
	"INSTALL_FAILED_NO_MATCHING_ABIS":         "The APK contains native code for a CPU architecture this Android runtime cannot run.",
	"INSTALL_FAILED_OLDER_SDK":                "The APK requires a newer Android version than the one running in the sandbox.",
	"INSTALL_FAILED_UPDATE_INCOMPATIBLE":      "A version of this app signed with a different key is already installed. Uninstall it first.",
	"INSTALL_FAILED_VERSION_DOWNGRADE":        "A newer version of this app is already installed. Uninstall it first or allow downgrades.",
	"INSTALL_PARSE_FAILED_NO_CERTIFICATES":    "The APK is not signed (or its signature is invalid). Sign it with apksigner.",
	"INSTALL_FAILED_INSUFFICIENT_STORAGE":     "The Android sandbox does not have enough free storage for this app.",
	"INSTALL_FAILED_INVALID_APK":              "The APK file is invalid or corrupt.",
	"INSTALL_FAILED_DUPLICATE_PERMISSION":     "The app declares a permission that another installed app already defines.",
	"INSTALL_FAILED_TEST_ONLY":                "The APK is marked test-only and was refused.",
	"INSTALL_FAILED_MISSING_SPLIT":            "The app is split into several APKs; install the base APK together with all its splits.",
	"INSTALL_PARSE_FAILED_NOT_APK":            "The file is not an APK.",
	CodeDeprecatedSDK:                         "The app targets a very old Android version (API < 23), which Android 14+ blocks. Install with --bypass-low-target-sdk-block.",
	"INSTALL_FAILED_ALREADY_EXISTS":           "The app is already installed.",
	"INSTALL_FAILED_CONFLICTING_PROVIDER":     "The app declares a content provider that another installed app already uses.",
	"INSTALL_PARSE_FAILED_MANIFEST_MALFORMED": "The APK's AndroidManifest.xml is malformed.",
	"INSTALL_FAILED_VERIFICATION_FAILURE":     "Package verification failed.",
	"INSTALL_FAILED_ABORTED":                  "The installation was aborted.",
	CodeUnknown:                               "Android failed to install the APK.",
}

// InstallError is a PackageManager install failure.
type InstallError struct {
	Code   string // e.g. INSTALL_FAILED_OLDER_SDK
	Detail string // PackageManager's explanation, if any
	Raw    string // full pm output
}

// Message returns a user-facing explanation of the failure.
func (e *InstallError) Message() string {
	if m, ok := installMessages[e.Code]; ok {
		return m
	}
	return "Android refused to install the APK."
}

func (e *InstallError) Error() string {
	s := e.Message() + " (" + e.Code
	if e.Detail != "" && e.Detail != e.Code {
		s += ": " + e.Detail
	}
	return s + ")"
}

var failureRe = regexp.MustCompile(`(?s)Failure \[([A-Z0-9_]+)(?::\s*(.*?))?\]\s*$`)

// parsePMResult interprets pm install/uninstall/commit output.
func parsePMResult(r result) error {
	out := r.combined()
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "Success" {
			return nil
		}
	}
	if i := strings.Index(out, "Failure ["); i >= 0 {
		if m := failureRe.FindStringSubmatch(strings.TrimSpace(out[i:])); m != nil {
			return &InstallError{Code: m[1], Detail: strings.TrimSpace(m[2]), Raw: out}
		}
	}
	detail := out
	if detail == "" {
		detail = fmt.Sprintf("pm exited with code %d and no output", r.code)
	}
	return &InstallError{Code: CodeUnknown, Detail: firstLine(detail), Raw: out}
}

func isDeprecatedSDK(err error) bool {
	ie, ok := err.(*InstallError)
	return ok && ie.Code == CodeDeprecatedSDK
}

// InstallAPK uploads localPath and installs it with pm install -r -t -g.
// On INSTALL_FAILED_DEPRECATED_SDK_VERSION it retries once with
// --bypass-low-target-sdk-block. Failures are *InstallError.
func (d *Device) InstallAPK(ctx context.Context, localPath string, opts InstallOptions) error {
	remote, cleanup, err := d.upload(ctx, localPath)
	if err != nil {
		return err
	}
	defer cleanup()
	err = d.pmInstall(ctx, remote, opts)
	if isDeprecatedSDK(err) && !opts.BypassLowTargetSDK {
		opts.BypassLowTargetSDK = true
		err = d.pmInstall(ctx, remote, opts)
	}
	return err
}

func (d *Device) pmInstall(ctx context.Context, remote string, opts InstallOptions) error {
	r, err := d.run(ctx, "pm install "+opts.flags()+" "+quote(remote))
	if err != nil {
		return err
	}
	return parsePMResult(r)
}

// upload pushes a local file to a temporary device path and returns a
// cleanup function that deletes it.
func (d *Device) upload(ctx context.Context, localPath string) (string, func(), error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", nil, fmt.Errorf("opening APK: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("opening APK: %w", err)
	}
	remote, err := d.tempName("apkinspector-", ".apk")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		cctx, cancel := cleanupCtx(ctx)
		defer cancel()
		d.run(cctx, "rm -f "+quote(remote))
	}
	if err := d.sh.Push(ctx, f, st.Size(), remote, 0o644, st.ModTime()); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("uploading %s to the device: %w", filepath.Base(localPath), err)
	}
	return remote, cleanup, nil
}

var sessionRe = regexp.MustCompile(`\[(\d+)\]`)

// InstallMultiple installs a split APK set (base + splits) atomically with
// pm install-create / install-write / install-commit.
func (d *Device) InstallMultiple(ctx context.Context, paths []string, opts InstallOptions) error {
	if len(paths) == 0 {
		return fmt.Errorf("install: no APK files given")
	}
	type up struct {
		remote, name string
		size         int64
	}
	var ups []up
	for i, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("opening APK: %w", err)
		}
		remote, cleanup, err := d.upload(ctx, p)
		if err != nil {
			return err
		}
		defer cleanup()
		ups = append(ups, up{remote: remote, name: fmt.Sprintf("%d_%s", i, sanitizeName(filepath.Base(p))), size: st.Size()})
	}
	attempt := func(opts InstallOptions) error {
		r, err := d.run(ctx, "pm install-create "+opts.flags())
		if err != nil {
			return err
		}
		m := sessionRe.FindStringSubmatch(r.stdout)
		if r.code != 0 || !strings.Contains(r.stdout, "Success") || m == nil {
			return parsePMResult(r)
		}
		session, _ := strconv.Atoi(m[1])
		abandon := func() {
			cctx, cancel := cleanupCtx(ctx)
			defer cancel()
			d.run(cctx, fmt.Sprintf("pm install-abandon %d", session))
		}
		for _, u := range ups {
			r, err := d.run(ctx, fmt.Sprintf("pm install-write -S %d %d %s %s", u.size, session, quote(u.name), quote(u.remote)))
			if err != nil {
				abandon()
				return err
			}
			if !strings.Contains(r.stdout, "Success") {
				abandon()
				return parsePMResult(r)
			}
		}
		r, err = d.run(ctx, fmt.Sprintf("pm install-commit %d", session))
		if err != nil {
			return err
		}
		return parsePMResult(r)
	}
	err := attempt(opts)
	if isDeprecatedSDK(err) && !opts.BypassLowTargetSDK {
		opts.BypassLowTargetSDK = true
		err = attempt(opts)
	}
	return err
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

// Uninstall removes pkg for all users.
func (d *Device) Uninstall(ctx context.Context, pkg string) error {
	if err := checkPackage(pkg); err != nil {
		return err
	}
	r, err := d.run(ctx, "pm uninstall "+pkg)
	if err != nil {
		return err
	}
	if err := parsePMResult(r); err != nil {
		out := r.combined()
		notInstalled := strings.Contains(out, "not installed")
		if !notInstalled && strings.Contains(out, "DELETE_FAILED_INTERNAL_ERROR") {
			// Android reports unknown packages this way; confirm.
			installed, ierr := d.IsInstalled(ctx, pkg)
			notInstalled = ierr == nil && !installed
		}
		if notInstalled {
			return fmt.Errorf("uninstalling %s: %w", pkg, ErrNotInstalled)
		}
		return fmt.Errorf("uninstalling %s: %w", pkg, &CommandError{Cmd: "pm uninstall " + pkg, ExitCode: r.code, Output: out})
	}
	return nil
}
