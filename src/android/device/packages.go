package device

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Launch starts pkg's launcher activity and returns the started component
// ("" when the monkey fallback was used). The launcher is resolved with
// `cmd package resolve-activity` and started with `am start -W -n`; if that
// fails, `monkey -p pkg -c android.intent.category.LAUNCHER 1` is used.
func (d *Device) Launch(ctx context.Context, pkg string) (string, error) {
	if err := checkPackage(pkg); err != nil {
		return "", err
	}
	var firstErr error
	r, err := d.run(ctx, "cmd package resolve-activity --brief -c android.intent.category.LAUNCHER "+pkg)
	if err != nil {
		return "", err
	}
	if comp := parseResolvedComponent(r.stdout); r.code == 0 && comp != "" {
		if err := d.LaunchComponent(ctx, comp); err == nil {
			return comp, nil
		} else {
			firstErr = err
		}
	}
	r, err = d.run(ctx, "monkey -p "+pkg+" -c android.intent.category.LAUNCHER 1")
	if err != nil {
		return "", err
	}
	if r.code == 0 && strings.Contains(r.stdout, "Events injected: 1") {
		return "", nil
	}
	if firstErr != nil {
		return "", fmt.Errorf("launching %s: %w", pkg, firstErr)
	}
	return "", fmt.Errorf("launching %s: no launchable activity found (%s)", pkg, firstLine(r.combined()))
}

// parseResolvedComponent extracts "pkg/.Activity" from resolve-activity --brief output.
func parseResolvedComponent(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.Contains(l, "/") && !strings.ContainsAny(l, " =") {
			return l
		}
	}
	return ""
}

// LaunchComponent starts an explicit activity ("pkg/.Activity") and waits
// for it to be displayed.
func (d *Device) LaunchComponent(ctx context.Context, component string) error {
	pkg, _, ok := strings.Cut(component, "/")
	if !ok || checkPackage(pkg) != nil {
		return fmt.Errorf("invalid component %q", component)
	}
	r, err := d.run(ctx, "am start -W -n "+quote(component))
	if err != nil {
		return err
	}
	out := r.combined()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Error") {
			return &CommandError{Cmd: "am start -W -n " + component, ExitCode: r.code, Output: out}
		}
	}
	if r.code != 0 {
		return &CommandError{Cmd: "am start -W -n " + component, ExitCode: r.code, Output: out}
	}
	return nil
}

// ForceStop stops all processes of pkg.
func (d *Device) ForceStop(ctx context.Context, pkg string) error {
	if err := checkPackage(pkg); err != nil {
		return err
	}
	_, err := d.runOK(ctx, "am force-stop "+pkg)
	return err
}

// ClearData deletes all data of pkg (pm clear).
func (d *Device) ClearData(ctx context.Context, pkg string) error {
	if err := checkPackage(pkg); err != nil {
		return err
	}
	r, err := d.run(ctx, "pm clear "+pkg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(r.stdout) != "Success" {
		return fmt.Errorf("clearing data of %s: %w", pkg, &CommandError{Cmd: "pm clear " + pkg, ExitCode: r.code, Output: r.combined()})
	}
	return nil
}

// IsInstalled reports whether pkg is installed.
func (d *Device) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	if err := checkPackage(pkg); err != nil {
		return false, err
	}
	r, err := d.run(ctx, "pm path "+pkg)
	if err != nil {
		return false, err
	}
	return r.code == 0 && strings.Contains(r.stdout, "package:"), nil
}

// PackageVersion is version information from dumpsys package.
type PackageVersion struct {
	VersionCode int64
	VersionName string
	MinSDK      int
	TargetSDK   int
}

// PackageVersion returns the installed version of pkg, or ErrNotInstalled.
func (d *Device) PackageVersion(ctx context.Context, pkg string) (PackageVersion, error) {
	if err := checkPackage(pkg); err != nil {
		return PackageVersion{}, err
	}
	r, err := d.runOK(ctx, "dumpsys package "+pkg)
	if err != nil {
		return PackageVersion{}, err
	}
	v, ok := parseDumpsysVersion(r.stdout, pkg)
	if !ok {
		return PackageVersion{}, fmt.Errorf("%s: %w", pkg, ErrNotInstalled)
	}
	return v, nil
}

func parseDumpsysVersion(out, pkg string) (PackageVersion, bool) {
	var v PackageVersion
	header := "Package [" + pkg + "]"
	in, found := false, false
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Package [") {
			if found {
				break // only the first (active) package block
			}
			in = strings.HasPrefix(t, header)
			found = in
			continue
		}
		if !in {
			continue
		}
		if !strings.HasPrefix(line, "    ") && t != "" {
			break // dedented: next section
		}
		for _, f := range strings.Fields(t) {
			k, val, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch k {
			case "versionCode":
				if n, err := strconv.ParseInt(val, 10, 64); err == nil && v.VersionCode == 0 {
					v.VersionCode = n
				}
			case "minSdk":
				v.MinSDK, _ = strconv.Atoi(val)
			case "targetSdk":
				v.TargetSDK, _ = strconv.Atoi(val)
			case "versionName":
				if v.VersionName == "" {
					v.VersionName = strings.TrimPrefix(t, "versionName=")
				}
			}
		}
	}
	return v, found
}

// ListPackagesWithUID returns installed packages mapped to their Linux UID.
func (d *Device) ListPackagesWithUID(ctx context.Context) (map[string]int, error) {
	r, err := d.runOK(ctx, "pm list packages -U")
	if err != nil {
		return nil, err
	}
	return parsePackageList(r.stdout), nil
}

func parsePackageList(out string) map[string]int {
	m := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "package:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(l, "package:"))
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if u, ok := strings.CutPrefix(f, "uid:"); ok {
				u, _, _ = strings.Cut(u, ",")
				if n, err := strconv.Atoi(u); err == nil {
					m[fields[0]] = n
				}
			}
		}
	}
	return m
}

// maxInputChunk bounds a single `input text` argument.
const maxInputChunk = 200

// InputText types text into the focused view (used for paste). Spaces are
// sent as %s, newlines as ENTER key events. `input text` only supports
// printable ASCII; other characters yield an error. A literal "%s" in text
// is typed as a space (a limitation of the input command).
func (d *Device) InputText(ctx context.Context, text string) error {
	for _, r := range text {
		if (r < 0x20 || r > 0x7e) && r != '\n' && r != '\t' {
			return fmt.Errorf("typing text: character %q is not supported by Android's input command (printable ASCII only)", r)
		}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			if _, err := d.runOK(ctx, "input keyevent 66"); err != nil {
				return err
			}
		}
		for len(line) > 0 {
			n := min(len(line), maxInputChunk)
			chunk := strings.NewReplacer(" ", "%s", "\t", "%s").Replace(line[:n])
			line = line[n:]
			if _, err := d.runOK(ctx, "input text "+quote(chunk)); err != nil {
				return err
			}
		}
	}
	return nil
}

var componentRe = regexp.MustCompile(`^[A-Za-z][\w.]*/[\w.$]+$`)

// preferredHomes are chosen, in order, when several launchers are installed.
var preferredHomes = []string{"com.android.launcher3", "com.farmerbb.taskbar", "com.google.android.apps.nexuslauncher"}

// SetDefaultHome makes one installed launcher the default (so Android does
// not ask "Select a Home app") and returns the chosen component. It is a
// no-op returning "" when there is at most one launcher.
func (d *Device) SetDefaultHome(ctx context.Context) (string, error) {
	r, err := d.run(ctx, "cmd package query-activities --brief -a android.intent.action.MAIN -c android.intent.category.HOME")
	if err != nil {
		return "", err
	}
	var homes []string
	for _, l := range strings.Split(r.stdout, "\n") {
		l = strings.TrimSpace(l)
		if componentRe.MatchString(l) && !strings.Contains(l, "FallbackHome") && !strings.HasPrefix(l, "com.android.settings/") {
			homes = append(homes, l)
		}
	}
	if len(homes) <= 1 {
		return "", nil
	}
	choice := homes[0]
pick:
	for _, pref := range preferredHomes {
		for _, h := range homes {
			if strings.HasPrefix(h, pref+"/") {
				choice = h
				break pick
			}
		}
	}
	if _, err := d.runOK(ctx, "cmd package set-home-activity "+quote(choice)); err != nil {
		return "", err
	}
	return choice, nil
}

// SetRotation forces the display orientation (0 = portrait, 1 = 90° … 3 =
// 270°) the way the emulator's rotate button does: auto-rotation is turned
// off and user_rotation is set. The setting persists on the data disk.
func (d *Device) SetRotation(ctx context.Context, orientation int) error {
	if orientation < 0 || orientation > 3 {
		return fmt.Errorf("rotation must be 0-3, got %d", orientation)
	}
	_, err := d.runOK(ctx, fmt.Sprintf("settings put system accelerometer_rotation 0 && settings put system user_rotation %d", orientation))
	return err
}

// Rotation returns the current forced orientation (0 when unset).
func (d *Device) Rotation(ctx context.Context) (int, error) {
	r, err := d.run(ctx, "settings get system user_rotation")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.stdout))
	if err != nil || n < 0 || n > 3 {
		return 0, nil
	}
	return n, nil
}
