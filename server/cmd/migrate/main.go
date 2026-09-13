package main

import (
	"database/sql"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL, err := config.DatabaseURL()
	if err != nil {
		logger.Error("load database configuration", "error", err)
		os.Exit(1)
	}
	migrationsDirectory := os.Getenv("MIGRATIONS_DIR")
	if migrationsDirectory == "" {
		migrationsDirectory = "migrations"
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		logger.Error("set migration dialect", "error", err)
		os.Exit(1)
	}
	if err := goose.Up(database, migrationsDirectory); err != nil {
		logger.Error("run migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations complete")
}
