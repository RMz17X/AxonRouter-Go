package oauth

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
)

// mimoDesktopPassToken is the account-session credential read from MiMo
// Desktop's cookie store: persisting it per connection is what lets
// multiple Xiaomi accounts rotate independently.
type mimoDesktopPassToken struct {
	passToken string
	userID    string
	cUserID   string
}

// readDesktopPassToken reads the persisted Xiaomi account cookies from
// MiMo Desktop's Electron profile. The Chromium cookie DB is held with an
// exclusive lock while Desktop runs, so it is copied first; any failure
// (missing file, locked copy, unreadable DB) yields nil, never an error.
func readDesktopPassToken() *mimoDesktopPassToken {
	src := mimoDesktopCookiePath()
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	tmp, err := os.CreateTemp("", "9r-mimo-cookies-*.db")
	if err != nil {
		return nil
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	raw, err := os.ReadFile(src)
	if err != nil {
		tmp.Close()
		return nil
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return nil
	}
	tmp.Close()
	db, err := sql.Open("sqlite", "file:"+tmpPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query("SELECT name, value FROM cookies WHERE host_key = ?", ".account.xiaomi.com")
	if err != nil {
		return nil
	}
	defer rows.Close()
	jar := map[string]string{}
	for rows.Next() {
		var name, value string
		if rows.Scan(&name, &value) == nil {
			jar[name] = value
		}
	}
	if jar["passToken"] == "" {
		return nil
	}
	return &mimoDesktopPassToken{
		passToken: jar["passToken"],
		userID:    jar["userId"],
		cUserID:   jar["cUserId"],
	}
}

// mimoDesktopCookiePath is MiMo Desktop's Chromium cookie DB per platform.
func mimoDesktopCookiePath() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Xiaomi MiMo", "Partitions", "xiaomi-account", "Network", "Cookies")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Xiaomi MiMo", "Partitions", "xiaomi-account", "Network", "Cookies")
	default:
		return filepath.Join(home, ".config", "Xiaomi MiMo", "Partitions", "xiaomi-account", "Network", "Cookies")
	}
}
