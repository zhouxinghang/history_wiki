package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
	"golang.org/x/term"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) != 2 || os.Args[1] != "create-admin" {
		fmt.Fprintln(os.Stderr, "用法: adminctl create-admin")
		os.Exit(2)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "create-admin 必须在交互式终端中运行")
		os.Exit(2)
	}
	databaseURL, err := config.DatabaseURL()
	if err != nil {
		logger.Error("load database configuration", "error", err)
		os.Exit(1)
	}
	passwordParams, err := config.LoadPasswordParams()
	if err != nil {
		logger.Error("load password configuration", "error", err)
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "管理员邮箱: ")
	email, err := reader.ReadString('\n')
	if err != nil {
		logger.Error("read administrator email", "error", err)
		os.Exit(1)
	}
	email = strings.TrimSpace(email)
	normalizedEmail, err := auth.NormalizeEmail(email)
	if err != nil {
		fmt.Fprintln(os.Stderr, "邮箱格式无效")
		os.Exit(2)
	}

	password, err := readPassword("密码（至少 12 个字符）: ")
	if err != nil {
		logger.Error("read password", "error", err)
		os.Exit(1)
	}
	confirmation, err := readPassword("再次输入密码: ")
	if err != nil {
		logger.Error("read password confirmation", "error", err)
		os.Exit(1)
	}
	if password != confirmation {
		fmt.Fprintln(os.Stderr, "两次输入的密码不一致")
		os.Exit(2)
	}
	passwordHash, err := auth.HashPassword(password, passwordParams)
	if err != nil {
		fmt.Fprintf(os.Stderr, "密码不符合要求: %v\n", err)
		os.Exit(2)
	}

	userID, err := auth.NewID()
	if err != nil {
		logger.Error("generate administrator ID", "error", err)
		os.Exit(1)
	}
	requestID, err := auth.NewID()
	if err != nil {
		logger.Error("generate audit request ID", "error", err)
		os.Exit(1)
	}

	database, err := store.Open(context.Background(), databaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	user := auth.User{
		ID:              userID,
		Email:           email,
		NormalizedEmail: normalizedEmail,
		PasswordHash:    passwordHash,
		Role:            auth.RoleAdministrator,
	}
	err = database.CreateFirstAdministrator(context.Background(), user, audit.Entry{
		RequestID:  "adminctl:" + requestID,
		Action:     "administrator.bootstrap",
		TargetType: "user",
		SourceIP:   "local",
		UserAgent:  "adminctl",
		Outcome:    audit.OutcomeSuccess,
		Details:    map[string]any{"role": auth.RoleAdministrator},
	})
	if errors.Is(err, auth.ErrAlreadyInitialized) {
		fmt.Fprintln(os.Stderr, "系统已存在账号；首位管理员命令只能成功执行一次")
		os.Exit(3)
	}
	if err != nil {
		logger.Error("create first administrator", "error", err)
		os.Exit(1)
	}

	fmt.Printf("已创建首位管理员 %s\n", email)
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
