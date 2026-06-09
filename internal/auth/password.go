package auth

import "golang.org/x/crypto/bcrypt"

// PasswordCost is the bcrypt cost for merchant dashboard passwords. Held at 12
// (build.md security rule: cost >= 12) — high enough to be expensive to brute
// force, low enough to keep login latency reasonable.
const PasswordCost = 12

// HashPassword returns a bcrypt hash of the plaintext password at PasswordCost.
// The hash embeds its own salt and cost, so it is self-describing for CheckPassword.
func HashPassword(plaintext string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), PasswordCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether plaintext matches the stored bcrypt hash.
// bcrypt's comparison is constant-time with respect to the hash.
func CheckPassword(plaintext, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}
