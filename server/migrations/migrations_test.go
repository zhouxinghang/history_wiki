package migrations_test

import (
	"math"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
)

func TestMigrationsAreDiscoverableAndMatchReadyVersion(t *testing.T) {
	migrations, err := goose.CollectMigrations(".", 0, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations found")
	}
	if got := migrations[len(migrations)-1].Version; got != config.LatestMigrationVersion {
		t.Fatalf("latest migration = %d, readiness expects %d", got, config.LatestMigrationVersion)
	}
}
