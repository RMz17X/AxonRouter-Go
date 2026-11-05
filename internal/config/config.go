package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/viper"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
)

// Config holds the proxy gateway configuration.
type Config struct {
	Host                string
	Port                int
	JWTSecret           string
	InitialPassword     string
	APIKeySecret        string
	MachineIDSalt       string
	RTKEnabled          bool
	CavemanEnabled      bool
	PonytailEnabled     bool
	ADHDEnabled         bool
	SeatAttemptBudgetMs int
}

// NewViper creates and configures a new Viper instance reading from .env with standard defaults.
func NewViper() *viper.Viper {
	return NewViperWithFile(".env")
}

// NewViperWithFile creates and configures a new Viper instance with the specified env file path.
func NewViperWithFile(configFile string) *viper.Viper {
	v := viper.New()
	if configFile != "" {
		v.SetConfigFile(configFile)
		v.SetConfigType("env")
	}

	v.AutomaticEnv()

	v.SetDefault("PORT", 20130)
	v.SetDefault("API_KEY_SECRET", "endpoint-proxy-api-key-secret")
	v.SetDefault("MACHINE_ID_SALT", "endpoint-proxy-salt")
	v.SetDefault("RTK_ENABLED", true)
	v.SetDefault("CAVEMAN_ENABLED", false)
	v.SetDefault("PONYTAIL_ENABLED", false)
	v.SetDefault("ADHD_ENABLED", false)
	v.SetDefault("SEAT_ATTEMPT_BUDGET_MS", 0)

	if configFile != "" {
		if err := v.ReadInConfig(); err != nil {
			var configFileNotFoundError viper.ConfigFileNotFoundError
			if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) && !errors.As(err, &configFileNotFoundError) {
				log.Warn("config", "read config file failed", "file", configFile, "error", err)
			}
		}
	}

	return v
}

// ProvideViper returns a configured Viper instance for dependency injection.
func ProvideViper() *viper.Viper {
	return NewViper()
}

// ProvideConfig provides *Config for dependency injection using the provided Viper instance.
func ProvideConfig(v *viper.Viper) *Config {
	return LoadConfigFromViper(v)
}

// ResolveDataDir returns the base data directory: DATA_DIR env, else the
// value from .env, else the platform default (~/.9router, or
// %APPDATA%/9router on Windows).
//
// The .env read is load-bearing, not a convenience. The server resolves its
// data dir through viper (LoadConfigFromViper), which does read .env, while
// `9router-go status|stop|logs` runs in a separate short-lived process that
// never boots the server. Reading only os.Getenv here meant a deployment
// configured purely through .env — every Docker/compose setup — had the CLI
// look at a different directory than the running daemon: status reported "not
// running" against a live listener, stop refused to kill it, and logs claimed
// no log existed. Both sides now answer the same question the same way.
func ResolveDataDir() string {
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		return dataDir
	}
	if dataDir := dataDirFromEnvFile(); dataDir != "" {
		return dataDir
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		if runtime.GOOS == "windows" {
			appData := os.Getenv("APPDATA")
			if appData == "" {
				appData = filepath.Join(homeDir, "AppData", "Roaming")
			}
			return filepath.Join(appData, "9router")
		}
		return filepath.Join(homeDir, ".9router")
	}
	return ".9router"
}

// dataDirFromEnvFile reads DATA_DIR out of the .env next to the binary or in
// the working directory. Viper owns the file format, so this reuses it rather
// than hand-parsing KEY=VALUE and drifting from what the server accepted.
func dataDirFromEnvFile() string {
	for _, path := range envFileCandidates() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		v := viper.New()
		v.SetConfigFile(path)
		v.SetConfigType("env")
		if err := v.ReadInConfig(); err != nil {
			continue
		}
		if dir := strings.TrimSpace(v.GetString("DATA_DIR")); dir != "" {
			return dir
		}
	}
	return ""
}

// envFileCandidates lists the .env locations the server itself reads: the one
// in the working directory, then the one beside the executable so a daemon
// started from another directory still finds the same config its CLI reports on.
func envFileCandidates() []string {
	candidates := []string{".env"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	return candidates
}

// LoadConfig loads the configuration from environment variables, .env file, and platform defaults using Viper.
func LoadConfig() *Config {
	return LoadConfigFromViper(NewViper())
}

// LoadConfigFromViper builds *Config using the given Viper instance.
func LoadConfigFromViper(v *viper.Viper) *Config {
	if v == nil {
		v = NewViper()
	}
	host := strings.TrimSpace(v.GetString("HOST"))
	if host == "" {
		host = strings.TrimSpace(v.GetString("BIND_ADDR"))
	}

	port := v.GetInt("PORT")
	if port <= 0 {
		port = 20130 // Default port (unified port)
	}

	dataDir := v.GetString("DATA_DIR")
	if dataDir == "" {
		dataDir = ResolveDataDir()
	}

	// Ensure the base data directory exists
	if err := os.MkdirAll(dataDir, constants.FilePermDir); err != nil {
		log.Warn("config", "create data dir failed", "dir", dataDir, "error", err)
	}

	// The database is configured by DATABASE_URL alone (read by
	// internal/db at boot); there is no database path setting anymore.

	// INITIAL_PASSWORD has no hardcoded default — an empty value forces the
	// operator to set one explicitly rather than shipping a known password.
	initialPassword := v.GetString("INITIAL_PASSWORD")

	apiKeySecret := v.GetString("API_KEY_SECRET")
	if apiKeySecret == "" {
		apiKeySecret = "endpoint-proxy-api-key-secret"
	}

	machineIDSalt := v.GetString("MACHINE_ID_SALT")
	if machineIDSalt == "" {
		machineIDSalt = "endpoint-proxy-salt"
	}

	rtkEnabled := v.GetBool("RTK_ENABLED")
	cavemanEnabled := v.GetBool("CAVEMAN_ENABLED")
	ponytailEnabled := v.GetBool("PONYTAIL_ENABLED")
	adhdEnabled := v.GetBool("ADHD_ENABLED")
	seatAttemptBudgetMs := v.GetInt("SEAT_ATTEMPT_BUDGET_MS")

	return &Config{
		Host:                host,
		Port:                port,
		JWTSecret:           loadJWTSecret(v),
		InitialPassword:     initialPassword,
		APIKeySecret:        apiKeySecret,
		MachineIDSalt:       machineIDSalt,
		RTKEnabled:          rtkEnabled,
		CavemanEnabled:      cavemanEnabled,
		PonytailEnabled:     ponytailEnabled,
		ADHDEnabled:         adhdEnabled,
		SeatAttemptBudgetMs: seatAttemptBudgetMs,
	}
}

// jwtEphemeralWarnOnce keeps the ephemeral-secret warning to once per
// process: LoadConfig runs on request paths, and a warning per request
// would bury the signal it carries.
var jwtEphemeralWarnOnce sync.Once

// jwtEphemeralSecret is the process-wide ephemeral JWT secret, generated
// on the first JWT_SECRET-unset LoadConfig call and returned by every
// later call. LoadConfig runs on request paths, so an unmemoized
// generate-per-call would mint a different secret for the request that
// signs a login than for the request that verifies it — sessions could
// never validate. A generation failure is deliberately NOT memoized:
// ephemeralJWTSecret keeps returning "" (callers fail closed) and a
// later call retries generation.
var (
	jwtEphemeralMu     sync.Mutex
	jwtEphemeralSecret string
)

// loadJWTSecret resolves the JWT signing secret. JWT_SECRET is the only
// durable source (P4 removed the per-node jwt-secret file): in a cluster
// every node must hold the SAME secret, and an env value is the only
// form that cannot drift per node. When it is unset the boot generates
// an ephemeral per-boot secret instead — sessions then do not survive
// a restart and do not span nodes, which the warning states loudly.
func loadJWTSecret(v *viper.Viper) string {
	var secret string
	if v != nil {
		secret = v.GetString("JWT_SECRET")
	}
	if secret == "" {
		secret = os.Getenv("JWT_SECRET")
	}
	if secret != "" {
		return secret
	}

	jwtEphemeralWarnOnce.Do(func() {
		log.Warn("config", "JWT_SECRET is not set; generated an ephemeral per-boot secret — sessions will NOT survive a restart and will NOT span nodes; set JWT_SECRET so every node shares one secret")
	})

	return ephemeralJWTSecret()
}

// ephemeralJWTSecret returns the process-wide ephemeral JWT secret,
// generating it on first use (see jwtEphemeralSecret). The value is
// never written anywhere: persisting it per node is exactly the drift
// the env-only rule exists to prevent.
func ephemeralJWTSecret() string {
	jwtEphemeralMu.Lock()
	defer jwtEphemeralMu.Unlock()
	if jwtEphemeralSecret != "" {
		return jwtEphemeralSecret
	}
	// Generate 32 cryptographically secure random bytes.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		log.Error("config", "crypto/rand failed to generate JWT secret; refusing to fall back to a static secret", "error", err)
		return ""
	}
	jwtEphemeralSecret = hex.EncodeToString(raw)
	return jwtEphemeralSecret
}
