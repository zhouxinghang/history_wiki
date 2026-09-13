package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/auth"
)

const LatestMigrationVersion int64 = 14

type Config struct {
	Address             string
	DatabaseURL         string
	PublicBaseURL       string
	SessionCookie       string
	CSRFCookie          string
	TrustedProxyCount   int
	LogLevel            slog.Level
	StaticDirectory     string
	DatabaseMaxConns    int32
	DatabaseMinConns    int32
	DatabaseMaxLifetime time.Duration
	DatabaseMaxIdleTime time.Duration
	SessionIdleTimeout  time.Duration
	SessionMaxLifetime  time.Duration
	PasswordParams      auth.Argon2idParams
}

func Load() (Config, error) {
	databaseURL, err := DatabaseURL()
	if err != nil {
		return Config{}, err
	}
	configuration := Config{
		Address:             envOrDefault("HTTP_ADDRESS", ":9003"),
		DatabaseURL:         databaseURL,
		PublicBaseURL:       strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		SessionCookie:       envOrDefault("SESSION_COOKIE_NAME", "__Host-history_wiki_session"),
		CSRFCookie:          envOrDefault("CSRF_COOKIE_NAME", "__Host-history_wiki_csrf"),
		StaticDirectory:     envOrDefault("STATIC_DIRECTORY", "/app/public"),
		DatabaseMaxConns:    20,
		DatabaseMinConns:    2,
		DatabaseMaxLifetime: 30 * time.Minute,
		DatabaseMaxIdleTime: 5 * time.Minute,
		SessionIdleTimeout:  8 * time.Hour,
		SessionMaxLifetime:  7 * 24 * time.Hour,
		PasswordParams:      auth.DefaultArgon2idParams(),
	}
	if configuration.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	if configuration.PublicBaseURL == "" {
		return Config{}, errors.New("PUBLIC_BASE_URL is required")
	}
	if configuration.TrustedProxyCount, err = intEnv("TRUSTED_PROXY_COUNT", 0, 0, 16); err != nil {
		return Config{}, err
	}
	if configuration.LogLevel, err = logLevelEnv("LOG_LEVEL", slog.LevelInfo); err != nil {
		return Config{}, err
	}
	if configuration.DatabaseMaxConns, err = int32Env("DATABASE_MAX_CONNS", configuration.DatabaseMaxConns, 1, 200); err != nil {
		return Config{}, err
	}
	if configuration.DatabaseMinConns, err = int32Env("DATABASE_MIN_CONNS", configuration.DatabaseMinConns, 0, configuration.DatabaseMaxConns); err != nil {
		return Config{}, err
	}
	if configuration.DatabaseMaxLifetime, err = durationEnv("DATABASE_MAX_CONN_LIFETIME", configuration.DatabaseMaxLifetime, time.Minute); err != nil {
		return Config{}, err
	}
	if configuration.DatabaseMaxIdleTime, err = durationEnv("DATABASE_MAX_CONN_IDLE_TIME", configuration.DatabaseMaxIdleTime, time.Minute); err != nil {
		return Config{}, err
	}
	if configuration.SessionIdleTimeout, err = durationEnv("SESSION_IDLE_TIMEOUT", configuration.SessionIdleTimeout, time.Minute); err != nil {
		return Config{}, err
	}
	if configuration.SessionMaxLifetime, err = durationEnv("SESSION_MAX_LIFETIME", configuration.SessionMaxLifetime, configuration.SessionIdleTimeout); err != nil {
		return Config{}, err
	}
	configuration.PasswordParams, err = LoadPasswordParams()
	if err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func LoadPasswordParams() (auth.Argon2idParams, error) {
	parameters := auth.DefaultArgon2idParams()
	var err error
	if parameters.Memory, err = uint32Env("ARGON2_MEMORY_KIB", parameters.Memory, 19*1024, 1024*1024); err != nil {
		return auth.Argon2idParams{}, err
	}
	if parameters.Iterations, err = uint32Env("ARGON2_ITERATIONS", parameters.Iterations, 1, 20); err != nil {
		return auth.Argon2idParams{}, err
	}
	parallelism, err := intEnv("ARGON2_PARALLELISM", int(parameters.Parallelism), 1, 32)
	if err != nil {
		return auth.Argon2idParams{}, err
	}
	parameters.Parallelism = uint8(parallelism)
	return parameters, nil
}

func DatabaseURL() (string, error) {
	value, err := secretValue("DATABASE_URL")
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	return value, nil
}

func secretValue(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	fileName := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if value != "" && fileName != "" {
		return "", fmt.Errorf("%s and %s_FILE cannot both be set", name, name)
	}
	if fileName == "" {
		return value, nil
	}
	contents, err := os.ReadFile(fileName)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	return strings.TrimSpace(string(contents)), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intEnv(name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func int32Env(name string, fallback, minimum, maximum int32) (int32, error) {
	value, err := intEnv(name, int(fallback), int(minimum), int(maximum))
	return int32(value), err
}

func uint32Env(name string, fallback, minimum, maximum uint32) (uint32, error) {
	value, err := intEnv(name, int(fallback), int(minimum), int(maximum))
	return uint32(value), err
}

func durationEnv(name string, fallback, minimum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum {
		return 0, fmt.Errorf("%s must be a duration of at least %s", name, minimum)
	}
	return value, nil
}

func logLevelEnv(name string, fallback slog.Level) (slog.Level, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s must be debug, info, warn, or error", name)
	}
	return level, nil
}
