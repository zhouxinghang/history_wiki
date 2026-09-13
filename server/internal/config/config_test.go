package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadProductionConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("PUBLIC_BASE_URL", "https://history.example/")
	t.Setenv("TRUSTED_PROXY_COUNT", "2")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("DATABASE_MAX_CONNS", "12")
	t.Setenv("DATABASE_MIN_CONNS", "1")
	t.Setenv("SESSION_IDLE_TIMEOUT", "2h")
	t.Setenv("SESSION_MAX_LIFETIME", "48h")
	t.Setenv("ARGON2_MEMORY_KIB", "32768")
	t.Setenv("ARGON2_ITERATIONS", "4")
	t.Setenv("ARGON2_PARALLELISM", "2")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicBaseURL != "https://history.example" || got.TrustedProxyCount != 2 || got.DatabaseMaxConns != 12 || got.SessionIdleTimeout != 2*time.Hour {
		t.Fatalf("configuration = %#v", got)
	}
	if got.PasswordParams.Memory != 32768 || got.PasswordParams.Iterations != 4 || got.PasswordParams.Parallelism != 2 {
		t.Fatalf("argon2 parameters = %#v", got.PasswordParams)
	}
}

func TestLoadDatabaseURLFromSecretFile(t *testing.T) {
	fileName := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(fileName, []byte("postgres://secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", fileName)
	t.Setenv("PUBLIC_BASE_URL", "https://history.example")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "postgres://secret" {
		t.Fatalf("database url = %q", got.DatabaseURL)
	}
}

func TestLoadRejectsMissingAndInvalidProductionConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing DATABASE_URL was accepted")
	}
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("PUBLIC_BASE_URL", "https://history.example")
	t.Setenv("DATABASE_MAX_CONNS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("invalid pool size was accepted")
	}
}
