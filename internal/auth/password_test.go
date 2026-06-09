package auth_test

import (
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/yahya-elkady/ledger/internal/auth"
)

func TestPasswordHashing(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !auth.CheckPassword("correct horse battery staple", hash) {
		t.Error("correct password should verify")
	}
	if auth.CheckPassword("wrong password", hash) {
		t.Error("wrong password should not verify")
	}

	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost < 12 {
		t.Errorf("bcrypt cost = %d, want >= 12", cost)
	}
}
