// Package auth centralizes password hashing and JWT issuance/verification
// for the ERP event bus. It intentionally has no dependency on db or handler
// so it can be imported anywhere (db seeding, handlers, middleware) without
// import cycles.
package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"erp-event-bus/types"
)

// ErrInvalidToken is returned for any token that fails parsing, signature
// verification, or expiry checks. Callers should treat this uniformly as
// "unauthenticated" rather than branching on the underlying cause.
var ErrInvalidToken = errors.New("invalid or expired token")

// AccessTokenTTL controls how long an issued JWT remains valid.
const AccessTokenTTL = 12 * time.Hour

// jwtSecret is loaded once from ERP_JWT_SECRET. A hardcoded fallback is used
// only so the binary still boots for local/dev use; production deployments
// MUST set ERP_JWT_SECRET to a long random value, or every token issued here
// is forgeable by anyone who reads this source file.
var jwtSecret = loadSecret()

func loadSecret() []byte {
	if s := os.Getenv("ERP_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	fmt.Println("WARNING: ERP_JWT_SECRET not set — using an insecure development default. " +
		"Set ERP_JWT_SECRET before deploying anywhere reachable outside localhost.")
	return []byte("dev-only-insecure-secret-change-me")
}

// Claims is the JWT payload. Roles is comma-separated to match the existing
// ContextRoles convention used by middleware.ModuleClearanceMiddleware and
// db.CheckPermission, so no reshaping is needed between the token and the
// request context.
type Claims struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Roles    string `json:"roles"`
	Region   string `json:"region"`
	jwt.RegisteredClaims
}

// ValidatePassword ensures password strength rules:
// - At least 8 characters
// - At least one uppercase letter [A-Z]
// - At least one lowercase letter [a-z]
// - At least one numeric digit [0-9]
// - At least one special character from the set: !@#$%^&*(),.?":{}|<>
func ValidatePassword(plaintext string) error {
	if len(plaintext) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range plaintext {
		switch {
		case ch >= 'A' && ch <= 'Z':
			hasUpper = true
		case ch >= 'a' && ch <= 'z':
			hasLower = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		case strings.ContainsRune("!@#$%^&*(),.?\":{}|<>", ch):
			hasSpecial = true
		}
	}
	if !hasUpper {
		return errors.New("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return errors.New("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return errors.New("password must contain at least one number")
	}
	if !hasSpecial {
		return errors.New("password must contain at least one special character")
	}
	return nil
}

// HashPassword bcrypt-hashes a plaintext password for storage. Never store
// or log the plaintext value it's called with.
func HashPassword(plaintext string) (string, error) {
	if err := ValidatePassword(plaintext); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether plaintext matches the given bcrypt hash.
func CheckPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// GenerateToken issues a signed JWT embedding the user's identity, tenant,
// roles, and region — everything ModuleClearanceMiddleware and downstream
// ABAC consumer checks need, so no extra DB lookup is required per request.
func GenerateToken(user *types.User) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   user.ID,
		TenantID: user.TenantID,
		Roles:    user.RoleName,
		Region:   user.Region,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ParseToken verifies signature and expiry and returns the embedded claims.
func ParseToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
