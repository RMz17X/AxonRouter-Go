package oauth

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ---- IDE credential store (zedCredentials.js port) ----

const zedDefaultCredentialsURL = "https://zed.dev"
const zedKeyringLabel = "zed-github-account"

// zedSettingsPaths lists Zed's settings.json locations per platform.
func zedSettingsPaths() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Zed", "settings.json")}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "Zed", "settings.json")}
	default:
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(xdg, "zed", "settings.json")}
	}
}

// resolveZedCredentialsURL reads credentials_url / server_url from Zed's
// settings, defaulting to https://zed.dev.
func resolveZedCredentialsURL() string {
	for _, p := range zedSettingsPaths() {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var settings map[string]any
		if json.Unmarshal(raw, &settings) != nil {
			continue
		}
		url := firstNonEmpty(jsonStringField(settings, "credentials_url"), jsonStringField(settings, "server_url"))
		if url != "" {
			return strings.TrimRight(url, "/")
		}
	}
	return zedDefaultCredentialsURL
}

// zedGlobalDBPaths lists the kv_store databases that may hold system_id.
func zedGlobalDBPaths() []string {
	home, _ := os.UserHomeDir()
	channels := []string{"0-global", "0-stable", "0-preview"}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "Zed", "db")
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		base = filepath.Join(local, "Zed", "db")
	default:
		xdg := os.Getenv("XDG_DATA_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(xdg, "zed", "db")
	}
	out := make([]string, 0, len(channels))
	for _, c := range channels {
		out = append(out, filepath.Join(base, c, "db.sqlite"))
	}
	return out
}

// readZedSystemID reads system_id from Zed's local kv_store, if present.
func readZedSystemID() string {
	for _, p := range zedGlobalDBPaths() {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		db, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
		if err != nil {
			continue
		}
		var v string
		qerr := db.QueryRow("SELECT value FROM kv_store WHERE key = ? LIMIT 1", "system_id").Scan(&v)
		db.Close()
		if qerr == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// zedIDECredentials is readZedIdeCredentials' outcome.
type zedIDECredentials struct {
	found          bool
	userID         string
	accessToken    string
	systemID       string
	credentialsURL string
	errText        string
}

// readZedIDECredentials reads the signed-in Zed IDE session from the OS
// secret store (Linux secret-tool, macOS Keychain, Windows Credential
// Manager), plus system_id from the local kv_store.
func readZedIDECredentials() zedIDECredentials {
	credentialsURL := resolveZedCredentialsURL()
	out := zedIDECredentials{credentialsURL: credentialsURL}
	var pair *zedCredentialPair
	switch runtime.GOOS {
	case "linux":
		pair = readZedLinuxCredentials(credentialsURL)
	case "darwin":
		pair = readZedMacCredentials(credentialsURL)
	case "windows":
		pair = readZedWindowsCredentials(credentialsURL)
	default:
		out.errText = fmt.Sprintf("Zed auto-import is not supported on platform %s", runtime.GOOS)
		return out
	}
	if pair == nil || pair.userID == "" || pair.accessToken == "" {
		out.errText = "Zed IDE session not found in the system keyring. Sign in to Zed, then retry."
		if pair != nil && pair.errText != "" {
			out.errText = pair.errText
		}
		return out
	}
	out.found = true
	out.userID = pair.userID
	out.accessToken = pair.accessToken
	out.systemID = firstNonEmpty(readZedSystemID(), randomUUID())
	return out
}

type zedCredentialPair struct {
	userID      string
	accessToken string
	label       string
	errText     string
}

// execWithTimeout runs a credential-store helper with Axon's timeouts.
func execWithTimeout(name string, timeout time.Duration, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

var (
	secretToolUsernameRe = regexp.MustCompile(`attribute\.username\s*=\s*(\S+)`)
	secretToolLabelRe    = regexp.MustCompile(`(?m)^\s*label\s*=\s*(.+)$`)
	secretToolSecretRe   = regexp.MustCompile(`(?m)^\s*secret\s*=\s*(.*)$`)
)

// parseSecretToolSearch mirrors Axon's parser: attribute lines ride on
// stderr, label/secret on stdout, and the whole dump collapses to at most
// one candidate item.
func parseSecretToolSearch(text string) *zedCredentialPair {
	username := ""
	if m := secretToolUsernameRe.FindStringSubmatch(text); m != nil {
		username = m[1]
	}
	label := ""
	if m := secretToolLabelRe.FindStringSubmatch(text); m != nil {
		label = strings.TrimSpace(m[1])
	}
	secret := ""
	if m := secretToolSecretRe.FindStringSubmatch(text); m != nil {
		secret = strings.TrimSpace(m[1])
	}
	if username == "" && secret == "" && label == "" {
		return nil
	}
	return &zedCredentialPair{userID: username, accessToken: secret, label: label}
}

// readZedLinuxCredentials reads Zed's secret-tool entry (url attribute,
// label zed-github-account), with Axon's lookup fallback for the username.
func readZedLinuxCredentials(credentialsURL string) *zedCredentialPair {
	stdout, stderr, err := execWithTimeout("secret-tool", 8*time.Second, "search", "--all", "url", credentialsURL)
	if err != nil {
		if isNotExistErr(err) {
			return &zedCredentialPair{errText: "secret-tool not found (install libsecret / secret-tools)"}
		}
		merged := stderr + "\n" + stdout
		if strings.Contains(merged, "secret =") {
			if item := parseSecretToolSearch(merged); item != nil && item.userID != "" {
				return item
			}
		}
	} else if item := parseSecretToolSearch(stderr + "\n" + stdout); item != nil {
		// Prefer the labelled GitHub-account entry; the parser collapses the
		// dump to one item, so a labelled match or any complete pair wins.
		if item.userID != "" && item.accessToken != "" {
			return item
		}
	}
	// Fallback: lookup returns the secret only; a second search finds the
	// username (user id).
	tokenOut, _, lerr := execWithTimeout("secret-tool", 8*time.Second, "lookup", "url", credentialsURL)
	if lerr != nil {
		if isNotExistErr(lerr) {
			return &zedCredentialPair{errText: "secret-tool not found (install libsecret / secret-tools)"}
		}
		return &zedCredentialPair{errText: lerr.Error()}
	}
	accessToken := strings.TrimSpace(tokenOut)
	if accessToken == "" {
		return &zedCredentialPair{errText: "Empty Zed keyring secret"}
	}
	sOut, sErr, _ := execWithTimeout("secret-tool", 8*time.Second, "search", "--all", "url", credentialsURL)
	userID := ""
	if item := parseSecretToolSearch(sErr + "\n" + sOut); item != nil {
		userID = item.userID
	}
	if userID == "" {
		return &zedCredentialPair{errText: "Zed keyring entry missing username (user id)"}
	}
	return &zedCredentialPair{userID: userID, accessToken: accessToken}
}

var (
	securityAcctRe     = regexp.MustCompile(`"acct"<blob>="([^"]*)"`)
	securityAcctHexRe  = regexp.MustCompile(`"acct"<blob>=0x[0-9A-Fa-f]+\s+"([^"]*)"`)
	securityPasswordRe = regexp.MustCompile(`(?m)password:\s*"(.*)"\s*$`)
)

// readZedMacCredentials reads the Keychain internet password Zed stores
// under its credentials URL (server), trying the bare host as fallback.
func readZedMacCredentials(credentialsURL string) *zedCredentialPair {
	servers := []string{credentialsURL, strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(credentialsURL, "https://"), "http://"), "/"), "zed.dev"}
	seen := map[string]bool{}
	lastError := ""
	for _, server := range servers {
		if server == "" || seen[server] {
			continue
		}
		seen[server] = true
		stdout, stderr, err := execWithTimeout("security", 8*time.Second, "find-internet-password", "-s", server, "-g")
		combined := stdout + "\n" + stderr
		password := parseSecurityPassword(combined)
		account := ""
		if m := securityAcctRe.FindStringSubmatch(combined); m != nil {
			account = m[1]
		} else if m := securityAcctHexRe.FindStringSubmatch(combined); m != nil {
			account = m[1]
		}
		if password != "" && account != "" {
			return &zedCredentialPair{userID: account, accessToken: password}
		}
		if password != "" && account == "" {
			lastError = "Found Zed keychain password but missing account (user id)"
		}
		if err != nil && lastError == "" {
			lastError = err.Error()
		}
	}
	if lastError == "" {
		lastError = "Zed credentials not found in macOS Keychain"
	}
	return &zedCredentialPair{errText: lastError}
}

// parseSecurityPassword extracts + unescapes the `password: "..."` dump.
func parseSecurityPassword(text string) string {
	if m := securityPasswordRe.FindStringSubmatch(text); m != nil {
		return regexp.MustCompile(`\\(.)`).ReplaceAllString(m[1], "$1")
	}
	if regexp.MustCompile(`(?m)password:\s*$`).MatchString(text) {
		return ""
	}
	return ""
}

// readZedWindowsCredentials reads the Credential Manager entry
// zed:url=<credentialsUrl> through PowerShell CredRead, like Axon.
func readZedWindowsCredentials(credentialsURL string) *zedCredentialPair {
	target := "zed:url=" + credentialsURL
	script := `
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
using System.Text;
public class ZedCred {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public uint Flags; public uint Type; public string TargetName; public string Comment;
    public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
    public uint CredentialBlobSize; public IntPtr CredentialBlob; public uint Persist;
    public uint AttributeCount; public IntPtr Attributes; public string TargetAlias; public string UserName;
  }
  [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
  public static extern bool CredRead(string target, uint type, uint reservedFlag, out IntPtr credentialPtr);
  [DllImport("advapi32.dll", SetLastError = true)]
  public static extern void CredFree(IntPtr buffer);
  public static string Read(string target) {
    IntPtr p;
    if (!CredRead(target, 1, 0, out p)) return null;
    try {
      var c = (CREDENTIAL)Marshal.PtrToStructure(p, typeof(CREDENTIAL));
      string secret = "";
      if (c.CredentialBlob != IntPtr.Zero && c.CredentialBlobSize > 0) {
        byte[] bytes = new byte[c.CredentialBlobSize];
        Marshal.Copy(c.CredentialBlob, bytes, 0, (int)c.CredentialBlobSize);
        secret = <redacted>);
      }
      return (c.UserName ?? "") + "\n" + secret;
    } finally { CredFree(p); }
  }
}
"@
$r = [ZedCred]::Read("` + target + `")
if ($null -eq $r) { exit 2 }
Write-Output $r
`
	stdout, _, err := execWithTimeout("powershell.exe", 15*time.Second, "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return &zedCredentialPair{errText: "Zed credentials not found in Windows Credential Manager"}
		}
		return &zedCredentialPair{errText: err.Error()}
	}
	text := strings.TrimPrefix(stdout, "\ufeff")
	nl := strings.Index(text, "\n")
	if nl < 0 {
		return &zedCredentialPair{errText: "Malformed Windows credential payload"}
	}
	userID := strings.TrimSpace(text[:nl])
	accessToken := strings.TrimRight(text[nl+1:], "\r\n")
	if userID == "" || accessToken == "" {
		return &zedCredentialPair{errText: "Windows Credential Manager entry missing username or secret"}
	}
	return &zedCredentialPair{userID: userID, accessToken: accessToken}
}

// isNotExistErr reports whether an exec failure is a missing binary.
func isNotExistErr(err error) bool {
	return err != nil && (os.IsNotExist(err) || strings.Contains(err.Error(), "executable file not found"))
}

// HandleZedAutoImport reads the signed-in Zed IDE session from the OS
