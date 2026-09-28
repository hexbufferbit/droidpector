// Package apk inspects Android application packages without a device: it
// validates the ZIP container and signature presence, decodes the binary
// AndroidManifest.xml (AXML) and resources.arsc, collects native ABIs and
// decides which runtime profile (ADR-004) should run the APK.
//
// All parsing is defensive: hostile input yields errors or validation issues,
// never panics or unbounded allocations.
package apk

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Size limits for entries that are read into memory.
const (
	maxManifestSize  = 16 << 20
	maxResourcesSize = 128 << 20
	maxZipEntries    = 200_000
)

// Severity of a validation Issue.
type Severity string

const (
	SeverityError   Severity = "error"   // Android will refuse to install the APK
	SeverityWarning Severity = "warning" // installable, but something is unusual
)

// IssueCode identifies a validation Issue.
type IssueCode string

const (
	IssueNotZip            IssueCode = "not_zip"
	IssueMissingManifest   IssueCode = "missing_manifest"
	IssueInvalidManifest   IssueCode = "invalid_manifest"
	IssueMissingPackage    IssueCode = "missing_package"
	IssueNoDex             IssueCode = "no_dex"
	IssueUnsigned          IssueCode = "unsigned"
	IssueV1OnlyTargetR     IssueCode = "v1_only_signature"
	IssueInvalidSigBlock   IssueCode = "invalid_signing_block"
	IssueSplitAPK          IssueCode = "split_apk"
	IssueARMOnly           IssueCode = "arm_only_native_code"
	IssueNoSupportedABI    IssueCode = "no_supported_abi"
	IssueResourcesUnusable IssueCode = "resources_unreadable"
	IssueNoLauncher        IssueCode = "no_launcher_activity"
	IssueTestOnly          IssueCode = "test_only"
	IssueLowTargetSDK      IssueCode = "low_target_sdk"
)

// Issue is one validation finding with a user-facing message.
type Issue struct {
	Severity Severity  `json:"severity"`
	Code     IssueCode `json:"code"`
	Message  string    `json:"message"`
}

// ValidationError is returned by Inspect when the file cannot be analysed at
// all (not a ZIP, no manifest, undecodable manifest).
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid APK"
	}
	return e.Issues[0].Message
}

// Info is the result of inspecting an APK.
type Info struct {
	Size int64 `json:"size"`

	Package            string   `json:"package"`
	VersionCode        int64    `json:"versionCode"` // includes versionCodeMajor in the upper 32 bits
	VersionName        string   `json:"versionName,omitempty"`
	MinSDK             int      `json:"minSdk"`
	TargetSDK          int      `json:"targetSdk"`
	MinSDKCodename     string   `json:"minSdkCodename,omitempty"` // preview SDK codename, e.g. "VanillaIceCream"
	Label              string   `json:"label,omitempty"`
	LaunchableActivity string   `json:"launchableActivity,omitempty"` // fully qualified class name
	Permissions        []string `json:"permissions,omitempty"`
	// ExtractNativeLibs is the android:extractNativeLibs value, nil when absent.
	ExtractNativeLibs *bool `json:"extractNativeLibs,omitempty"`
	Debuggable        bool  `json:"debuggable,omitempty"`
	TestOnly          bool  `json:"testOnly,omitempty"`
	// Split is the split name when this APK is a split of an app bundle.
	Split string `json:"split,omitempty"`

	ABIs      []string  `json:"abis,omitempty"` // native ABIs from lib/<abi>/*.so, sorted
	DexFiles  []string  `json:"dexFiles,omitempty"`
	Signature Signature `json:"signature"`

	Issues []Issue `json:"issues,omitempty"`
}

// IsSplit reports whether the APK is a split (not a base) APK.
func (i *Info) IsSplit() bool { return i.Split != "" }

// Installable reports whether no error-level issue was found.
func (i *Info) Installable() bool { return i.Err() == nil }

// Err returns a ValidationError holding the error-level issues, or nil.
func (i *Info) Err() error {
	var errs []Issue
	for _, is := range i.Issues {
		if is.Severity == SeverityError {
			errs = append(errs, is)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Issues: errs}
}

func (i *Info) addIssue(sev Severity, code IssueCode, format string, args ...any) {
	i.Issues = append(i.Issues, Issue{Severity: sev, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Inspect opens and inspects the APK at path.
func Inspect(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening APK: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("opening APK: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("opening APK: %s is not a regular file", path)
	}
	return InspectReader(f, st.Size())
}

// InspectReader inspects an APK of the given size. Fatal structural problems
// are reported as a *ValidationError; everything else becomes Info.Issues.
func InspectReader(r io.ReaderAt, size int64) (*Info, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fatal(IssueNotZip, "This file is not a valid APK: it is not a ZIP archive (%v).", err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, fatal(IssueNotZip, "This file is not a valid APK: it has too many entries (%d).", len(zr.File))
	}
	info := &Info{Size: size}

	var manifest, resources *zip.File
	abis := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		switch {
		case name == "AndroidManifest.xml":
			manifest = f
		case name == "resources.arsc":
			resources = f
		case !strings.Contains(name, "/") && strings.HasPrefix(name, "classes") && strings.HasSuffix(name, ".dex"):
			info.DexFiles = append(info.DexFiles, name)
		case strings.HasPrefix(name, "lib/") && strings.HasSuffix(name, ".so"):
			parts := strings.Split(name, "/")
			if len(parts) == 3 && parts[1] != "" && parts[2] != "" {
				abis[parts[1]] = true
			}
		case isV1SignatureFile(name):
			info.Signature.V1 = true
		}
	}
	if manifest == nil {
		return nil, fatal(IssueMissingManifest, "This file is not a valid APK: AndroidManifest.xml is missing.")
	}
	for a := range abis {
		info.ABIs = append(info.ABIs, a)
	}
	sort.Strings(info.ABIs)
	sort.Strings(info.DexFiles)

	mb, err := readEntry(manifest, maxManifestSize)
	if err != nil {
		return nil, fatal(IssueInvalidManifest, "This APK's AndroidManifest.xml cannot be read: %v.", err)
	}
	root, err := ParseAXML(mb)
	if err != nil {
		return nil, fatal(IssueInvalidManifest, "This APK's AndroidManifest.xml is corrupt: %v.", err)
	}
	if root.Name != "manifest" {
		return nil, fatal(IssueInvalidManifest, "This APK's AndroidManifest.xml is corrupt: root element is <%s>, not <manifest>.", root.Name)
	}

	var table *resourceTable
	if resources != nil {
		if rb, err := readEntry(resources, maxResourcesSize); err != nil {
			info.addIssue(SeverityWarning, IssueResourcesUnusable, "resources.arsc could not be read (%v); resource names such as the app label may be missing.", err)
		} else if table, err = parseResourceTable(rb); err != nil {
			info.addIssue(SeverityWarning, IssueResourcesUnusable, "resources.arsc is malformed (%v); resource names such as the app label may be missing.", err)
			table = nil
		}
	}
	info.applyManifest(root, table)
	info.checkSignature(r, size)
	info.validate()
	return info, nil
}

func fatal(code IssueCode, format string, args ...any) error {
	return &ValidationError{Issues: []Issue{{Severity: SeverityError, Code: code, Message: fmt.Sprintf(format, args...)}}}
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s is too large (%d bytes, limit %d)", f.Name, f.UncompressedSize64, limit)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is too large (limit %d bytes)", f.Name, limit)
	}
	return b, nil
}

func (i *Info) checkSignature(r io.ReaderAt, size int64) {
	eocd, err := FindEOCD(r, size)
	if err != nil {
		i.addIssue(SeverityWarning, IssueInvalidSigBlock, "Could not locate the ZIP directory to check for v2/v3 signatures: %v.", err)
		return
	}
	sb, err := ReadSigningBlock(r, eocd)
	if err != nil {
		i.addIssue(SeverityError, IssueInvalidSigBlock, "The APK signing block is corrupt (%v); Android will reject this APK. Re-sign it with apksigner.", err)
		return
	}
	if sb != nil {
		_, i.Signature.V2 = sb.Pairs[SigBlockIDV2]
		_, i.Signature.V3 = sb.Pairs[SigBlockIDV3]
		_, i.Signature.V31 = sb.Pairs[SigBlockIDV31]
	}
}

// validate derives issues from the collected information.
func (i *Info) validate() {
	if i.Package == "" {
		i.addIssue(SeverityError, IssueMissingPackage, "The manifest does not declare a package name; Android will reject this APK.")
	}
	if len(i.DexFiles) == 0 {
		i.addIssue(SeverityWarning, IssueNoDex, "This APK contains no classes.dex. It is either a resource-only split or a broken build; an app without code cannot be launched.")
	}
	switch {
	case !i.Signature.Signed():
		i.addIssue(SeverityError, IssueUnsigned, "This APK is not signed. Android refuses to install unsigned APKs (INSTALL_PARSE_FAILED_NO_CERTIFICATES). Sign it with apksigner or a debug key.")
	case i.Signature.V1 && !i.Signature.V2 && !i.Signature.V3 && !i.Signature.V31 && i.TargetSDK >= 30:
		i.addIssue(SeverityError, IssueV1OnlyTargetR, "This APK targets Android 11+ (API %d) but only has a v1 (JAR) signature. Android requires an APK Signature Scheme v2 or newer signature; re-sign it with apksigner.", i.TargetSDK)
	}
	if i.IsSplit() {
		i.addIssue(SeverityWarning, IssueSplitAPK, "This is a split APK (%q) of an app bundle. Install it together with its base APK and the other splits.", i.Split)
	}
	if i.TestOnly {
		i.addIssue(SeverityWarning, IssueTestOnly, "This APK is marked testOnly; it is installed with the -t flag.")
	}
	if i.TargetSDK > 0 && i.TargetSDK < 23 {
		i.addIssue(SeverityWarning, IssueLowTargetSDK, "This APK targets API %d. Android 14 and newer refuse apps targeting API < 23 unless installed with --bypass-low-target-sdk-block.", i.TargetSDK)
	}
	if !i.IsSplit() && len(i.DexFiles) > 0 && i.LaunchableActivity == "" {
		i.addIssue(SeverityWarning, IssueNoLauncher, "This APK has no launcher activity, so it cannot be started from the home screen.")
	}
	if len(i.ABIs) > 0 {
		supported, arm := i.abiClasses()
		switch {
		case !supported && len(arm) > 0:
			i.addIssue(SeverityWarning, IssueARMOnly, "This APK contains native code only for ARM (%s). It needs the ARM translation layer of the x86_64 runtime, or the slower ARM64 runtime.", strings.Join(arm, ", "))
		case !supported:
			i.addIssue(SeverityError, IssueNoSupportedABI, "This APK contains native code only for unsupported CPU architectures (%s); no available runtime can run it.", strings.Join(i.ABIs, ", "))
		}
	}
}

// abiClasses reports whether an x86/x86_64 ABI is present and lists ARM ABIs.
func (i *Info) abiClasses() (x86 bool, arm []string) {
	for _, a := range i.ABIs {
		switch a {
		case "x86_64", "x86":
			x86 = true
		case "arm64-v8a", "armeabi-v7a", "armeabi":
			arm = append(arm, a)
		}
	}
	return x86, arm
}

// Runtime profile names (ADR-004).
const (
	RuntimeX86_64 = "x86_64"
	RuntimeARM64  = "arm64"
)

// RuntimeDecision says which runtime profile should run an APK.
type RuntimeDecision struct {
	Runtime    string `json:"runtime"`    // RuntimeX86_64 or RuntimeARM64
	Translated bool   `json:"translated"` // ARM code runs through the native-bridge translation layer
	Reason     string `json:"reason"`
}

// RequiredRuntime implements the ADR-004 ABI placement. APKs without native
// code or with x86/x86_64 code run on the x86_64 runtime. ARM-only APKs run
// on x86_64 with Translated=true when translationAvailable is set (the
// x86_64 guest has a native-bridge translator such as libndk_translation,
// i.e. its ro.product.cpu.abilist includes arm64-v8a/armeabi-v7a), and on the
// arm64 runtime profile otherwise.
func (i *Info) RequiredRuntime(translationAvailable bool) RuntimeDecision {
	if len(i.ABIs) == 0 {
		return RuntimeDecision{Runtime: RuntimeX86_64, Reason: "The APK has no native code, so it runs on the fast x86_64 runtime."}
	}
	x86, arm := i.abiClasses()
	if x86 {
		return RuntimeDecision{Runtime: RuntimeX86_64, Reason: "The APK includes x86 native code (" + strings.Join(i.ABIs, ", ") + "), so it runs on the fast x86_64 runtime."}
	}
	if len(arm) == 0 {
		return RuntimeDecision{Runtime: RuntimeX86_64, Reason: "The APK's native code (" + strings.Join(i.ABIs, ", ") + ") matches no supported runtime; trying the x86_64 runtime, where installation will likely fail."}
	}
	if translationAvailable {
		return RuntimeDecision{Runtime: RuntimeX86_64, Translated: true, Reason: "The APK has ARM-only native code (" + strings.Join(arm, ", ") + "); it runs on the x86_64 runtime through the ARM translation layer."}
	}
	return RuntimeDecision{Runtime: RuntimeARM64, Reason: "The APK has ARM-only native code (" + strings.Join(arm, ", ") + "); it runs on the ARM64 runtime, which is emulated and noticeably slower. An x86_64 runtime with ARM translation would be faster."}
}

// applyManifest extracts the fields of interest from the decoded manifest.
func (i *Info) applyManifest(root *XMLElement, table *resourceTable) {
	str := func(v Value) string {
		if v.IsReference() {
			if rv, ok := table.resolve(v); ok {
				return rv.Text()
			}
		}
		return v.Text()
	}
	if v, ok := root.Attr("", "package"); ok {
		i.Package = strings.TrimSpace(str(v))
	}
	if v, ok := root.Attr("", "split"); ok {
		i.Split = str(v)
	}
	var code, major int64
	if v, ok := root.Attr(AndroidNS, "versionCode"); ok {
		if rv, ok := table.resolve(v); ok {
			v = rv
		}
		if n, ok := v.Int(); ok {
			code = n & 0xFFFFFFFF
		}
	}
	if v, ok := root.Attr(AndroidNS, "versionCodeMajor"); ok {
		if n, ok := v.Int(); ok {
			major = n & 0xFFFFFFFF
		}
	}
	i.VersionCode = major<<32 | code
	if v, ok := root.Attr(AndroidNS, "versionName"); ok {
		i.VersionName = str(v)
	}
	for _, us := range root.ChildrenNamed("uses-sdk") {
		if v, ok := us.Attr(AndroidNS, "minSdkVersion"); ok {
			if n, ok := v.Int(); ok {
				i.MinSDK = int(n)
			} else if v.Type == TypeString {
				i.MinSDKCodename = v.String
			}
		}
		if v, ok := us.Attr(AndroidNS, "targetSdkVersion"); ok {
			if n, ok := v.Int(); ok {
				i.TargetSDK = int(n)
			}
		}
	}
	if i.MinSDK == 0 && i.MinSDKCodename == "" {
		i.MinSDK = 1 // Android's default when uses-sdk/minSdkVersion is absent
	}
	if i.TargetSDK == 0 {
		i.TargetSDK = i.MinSDK
	}
	seen := map[string]bool{}
	for _, tag := range []string{"uses-permission", "uses-permission-sdk-23", "uses-permission-sdk-m"} {
		for _, p := range root.ChildrenNamed(tag) {
			if v, ok := p.Attr(AndroidNS, "name"); ok {
				if n := str(v); n != "" && !seen[n] {
					seen[n] = true
					i.Permissions = append(i.Permissions, n)
				}
			}
		}
	}
	apps := root.ChildrenNamed("application")
	if len(apps) == 0 {
		return
	}
	app := apps[0]
	if v, ok := app.Attr(AndroidNS, "label"); ok {
		i.Label = str(v)
	}
	if v, ok := app.Attr(AndroidNS, "extractNativeLibs"); ok {
		if b, ok := v.Bool(); ok {
			i.ExtractNativeLibs = &b
		}
	}
	if v, ok := app.Attr(AndroidNS, "debuggable"); ok {
		i.Debuggable, _ = v.Bool()
	}
	if v, ok := app.Attr(AndroidNS, "testOnly"); ok {
		i.TestOnly, _ = v.Bool()
	}
	i.LaunchableActivity = findLauncher(app, i.Package, str)
}

func findLauncher(app *XMLElement, pkg string, str func(Value) string) string {
	for _, c := range app.Children {
		if c.Name != "activity" && c.Name != "activity-alias" {
			continue
		}
		if v, ok := c.Attr(AndroidNS, "enabled"); ok {
			if b, ok := v.Bool(); ok && !b {
				continue
			}
		}
		for _, f := range c.ChildrenNamed("intent-filter") {
			if hasNamed(f, "action", "android.intent.action.MAIN", str) &&
				hasNamed(f, "category", "android.intent.category.LAUNCHER", str) {
				if v, ok := c.Attr(AndroidNS, "name"); ok {
					return qualifyClass(pkg, str(v))
				}
			}
		}
	}
	return ""
}

func hasNamed(e *XMLElement, tag, want string, str func(Value) string) bool {
	for _, c := range e.ChildrenNamed(tag) {
		if v, ok := c.Attr(AndroidNS, "name"); ok && str(v) == want {
			return true
		}
	}
	return false
}

// qualifyClass expands ".Main" / "Main" relative class names like PackageParser.
func qualifyClass(pkg, name string) string {
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, "."):
		return pkg + name
	case !strings.Contains(name, "."):
		return pkg + "." + name
	}
	return name
}

// Component returns the launcher component in "pkg/class" form for am start.
func (i *Info) Component() string {
	if i.LaunchableActivity == "" || i.Package == "" {
		return ""
	}
	return i.Package + "/" + i.LaunchableActivity
}

// IsValidationError reports whether err is (or wraps) a *ValidationError.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}
