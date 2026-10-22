package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidPassword = errors.New("invalid password")
	ErrPasswordTooShort = errors.New("password must be at least 6 characters long")
)

// HashPassword hashes a plaintext password using bcrypt with standard default cost.
func HashPassword(password string) (string, error) {
	if len(password) < 6 {
		return "", ErrPasswordTooShort
	}

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}

	return string(bytes), nil
}

// CheckPassword compares a bcrypt hashed password with its possible plaintext equivalent.
func CheckPassword(hashedPassword, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrInvalidPassword
		}
		return fmt.Errorf("password verification error: %w", err)
	}
	return nil
}
