package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"erp-event-bus/auth"
	"erp-event-bus/db"
	"erp-event-bus/email"
	"erp-event-bus/middleware"
)

// AuthHandler serves the public registration/login surface and the
// authenticated /me endpoint. Kept synchronous (no NATS involvement) since
// identity issuance is security-critical and callers need an immediate,
// consistent answer rather than an eventually-consistent one.
type AuthHandler struct {
	db       *db.Database
	sqliteDB *db.SQLiteDB
	emailSvc *email.EmailService
}

func NewAuthHandler(database *db.Database, sdb *db.SQLiteDB, emailSvc *email.EmailService) *AuthHandler {
	return &AuthHandler{db: database, sqliteDB: sdb, emailSvc: emailSvc}
}

type RegisterPayload struct {
	// TenantID identifies an existing tenant to join. Leave empty (and set
	// TenantName) to bootstrap a brand-new tenant instead.
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	Region     string `json:"region"`
}

type LoginPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id"`
}

type authResponse struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	RoleName string `json:"role_name"`
	Region   string `json:"region"`
}

// RegisterHandler is the public self-service signup endpoint. It never
// trusts a client-supplied role — see db.RegisterUserTransaction for the
// role-assignment policy that keeps this safe to leave unauthenticated.
func (h *AuthHandler) RegisterHandler(c echo.Context) error {
	var p RegisterPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	p.Email = strings.TrimSpace(p.Email)
	p.Name = strings.TrimSpace(p.Name)
	if p.Email == "" || p.Name == "" || p.Password == "" || p.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name, email, password, and region are required"})
	}
	if p.TenantID == "" && p.TenantName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provide tenant_id to join an existing tenant, or tenant_name to create one"})
	}

	hash, err := auth.HashPassword(p.Password)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	tenantID := p.TenantID
	if tenantID == "" {
		// Deterministic-ish but unique enough for this in-memory store; swap
		// for a proper slug/collision check once this moves to Postgres.
		tenantID = "tenant_" + uuid.NewString()[:8]
	}
	userID := "usr_" + uuid.NewString()[:12]

	user, err := h.db.RegisterUserTransaction(userID, tenantID, p.TenantName, p.Name, p.Email, p.Region, hash)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}

	token, err := auth.GenerateToken(user)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to issue token"})
	}

	h.sqliteDB.Log(user.TenantID, user.ID, "Register_Success",
		fmt.Sprintf("User %s registered as %s under tenant %s", user.Email, user.RoleName, user.TenantID))

	return c.JSON(http.StatusCreated, authResponse{
		Token: token, UserID: user.ID, TenantID: user.TenantID,
		Name: user.Name, Email: user.Email, RoleName: user.RoleName, Region: user.Region,
	})
}

// LoginHandler exchanges email+password for a JWT. Deliberately returns the
// same generic error for "no such user" and "wrong password" so the
// endpoint doesn't leak which emails are registered.
func (h *AuthHandler) LoginHandler(c echo.Context) error {
	var p LoginPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	const genericErr = "invalid email or password"

	user, err := h.db.GetUserByEmail(p.Email)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": genericErr})
	}
	if p.TenantID != "" && user.TenantID != p.TenantID {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid tenant space, email or password"})
	}
	if !auth.CheckPassword(user.PasswordHash, p.Password) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": genericErr})
	}

	token, err := auth.GenerateToken(user)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to issue token"})
	}

	h.sqliteDB.Log(user.TenantID, user.ID, "Login_Success", fmt.Sprintf("User %s logged in", user.Email))

	// Trigger Email Notification for login confirmation
	_ = h.emailSvc.SendLoginConfirmation(user.Email, user.Name, time.Now().Format(time.RFC1123), user.Region)

	return c.JSON(http.StatusOK, authResponse{
		Token: token, UserID: user.ID, TenantID: user.TenantID,
		Name: user.Name, Email: user.Email, RoleName: user.RoleName, Region: user.Region,
	})
}

// MeHandler returns the caller's own identity as resolved from their JWT —
// this is what the frontend calls once at load time to decide which role's
// UI to render, rather than trusting anything cached client-side.
func (h *AuthHandler) MeHandler(c echo.Context) error {
	userID, _ := c.Get(middleware.ContextUserID).(string)
	user, err := h.db.GetUser(userID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "user not found"})
	}

	return c.JSON(http.StatusOK, authResponse{
		UserID: user.ID, TenantID: user.TenantID, Name: user.Name,
		Email: user.Email, RoleName: user.RoleName, Region: user.Region,
	})
}
