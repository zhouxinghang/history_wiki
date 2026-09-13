package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashUsesArgon2idAndVerifies(t *testing.T) {
	params := DefaultArgon2idParams()
	params.Memory = 1024
	params.Iterations = 1

	hash, err := HashPassword("correct horse battery staple", params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q", hash)
	}

	matched, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !matched {
		t.Fatalf("matched = %t, err = %v", matched, err)
	}
	matched, err = VerifyPassword(hash, "wrong password")
	if err != nil || matched {
		t.Fatalf("wrong password matched = %t, err = %v", matched, err)
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("error = %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", maximumPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("error = %v", err)
	}
}
