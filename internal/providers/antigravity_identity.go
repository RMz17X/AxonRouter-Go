package providers

import (
	"fmt"
	"runtime"
)

// This file is the one definition of the Antigravity client identity
// AxonRouter-Go presents: the standalone Antigravity CLI (`agy`), mirrored
// from the official v1.5.1 build (teardown evidence graded in
// docs/ANTIGRAVITY_SUSPEND_ANALYSIS.md §13). The IDE-derived profile is
// gone — every Antigravity surface (chat, media, dashboard, discovery,
// OAuth) presents this CLI identity, because presenting several client
// shapes for one account is the fingerprint drift that got accounts
// suspended. A clientProfile value stored on an old connection is inert
// legacy data: it is never read and never changes what is sent.
//
// The real CLI builds its User-Agent at runtime:
//
//	antigravity/cli/<version> (aidev_client; os_type=<OS>; arch=<arch>;
//	    cl=<changelist>; auth_method=<method>)
//
// with os_type/arch read from the running build's platform globals, never
// a hardcoded foreign OS. The values below follow the same rule from this
// process's runtime: a Linux build presents linux, a Windows build
// windows, on the architecture it was compiled for.

// AntigravityUserAgent returns the User-Agent every Antigravity request
// carries, in the real CLI's grammar. auth_method=consumer is the OAuth
// flavour every router sign-in uses; agy's other method ("gcp",
// first-party) also selects the other embedded OAuth client and is not
// one this router can present.
func AntigravityUserAgent() string {
	return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=%s; arch=%s; cl=%d; auth_method=consumer)",
		AntigravityCLIVersion, runtime.GOOS, runtime.GOARCH, AntigravityCLIChangelist)
}

// AntigravityPlatform returns the loadCodeAssist ClientMetadata platform
// enum for this process's platform, derived from the same runtime OS/arch
// the User-Agent reports: DARWIN_AMD64=1, DARWIN_ARM64=2, LINUX_AMD64=3,
// LINUX_ARM64=4, WINDOWS_AMD64=5. Combinations the enum has no value for
// (e.g. windows/arm64) report PLATFORM_UNSPECIFIED (0) rather than
// borrowing another platform's number.
func AntigravityPlatform() int {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		return 1
	case "darwin/arm64":
		return 2
	case "linux/amd64":
		return 3
	case "linux/arm64":
		return 4
	case "windows/amd64":
		return 5
	}
	return 0
}

// AntigravityLoadCodeAssistMetadata returns the ClientMetadata body every
// loadCodeAssist / onboardUser call sends, in the real CLI's numeric
// shape: ideType ANTIGRAVITY=9, pluginType GEMINI=2, the mirrored CLI
// version as ideVersion, ideName "antigravity", and the runtime platform
// above. pluginVersion, updateChannel and duetProject are omitted on
// purpose: the teardown could not establish what the real client sends
// there, and an omitted field is honest where an invented value would
// itself be a fingerprint.
func AntigravityLoadCodeAssistMetadata() map[string]any {
	return map[string]any{
		"ideType":    9, // ANTIGRAVITY
		"ideVersion": AntigravityCLIVersion,
		"platform":   AntigravityPlatform(),
		"pluginType": 2, // GEMINI
		"ideName":    "antigravity",
	}
}
