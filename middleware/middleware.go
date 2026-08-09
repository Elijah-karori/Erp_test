package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"erp-event-bus/auth"
	"erp-event-bus/db"
)

const (
	ContextTenantID = "tenant_id"
	ContextUserID   = "user_id"
	ContextRoles    = "roles"
	ContextRegion   = "region"
)

// JWTAuthMiddleware verifies a signed bearer token and populates the same
// context keys MockAuthMiddleware used to set from raw headers — so
// ModuleClearanceMiddleware and every downstream handler work unchanged.
// This is what actually protects the API; identity now comes from a
// verified token, not from headers the caller can set to anything.
func JWTAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing or malformed Authorization header")
			}
			tokenString := strings.TrimPrefix(header, "Bearer ")

			claims, err := auth.ParseToken(tokenString)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired token")
			}

			c.Set(ContextTenantID, claims.TenantID)
			c.Set(ContextUserID, claims.UserID)
			c.Set(ContextRoles, claims.Roles)
			c.Set(ContextRegion, claims.Region)

			return next(c)
		}
	}
}

// MockAuthMiddleware extracts multi-tenant and user credentials directly
// from request headers with no verification at all. It is kept only
// because test/integration_test.go depends on it — it must never be wired
// into a real route again (see JWTAuthMiddleware for the replacement).
func MockAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tenantID := c.Request().Header.Get("Authorization-Tenant-Id")
			userID := c.Request().Header.Get("Authorization-User-Id")
			roles := c.Request().Header.Get("Authorization-Roles")
			region := c.Request().Header.Get("Authorization-Region")

			if tenantID == "" {
				tenantID = "tenant_safari" // default seed
			}
			if userID == "" {
				userID = "anonymous"
			}
			if roles == "" {
				roles = "guest"
			}
			if region == "" {
				region = "unknown"
			}

			c.Set(ContextTenantID, tenantID)
			c.Set(ContextUserID, userID)
			c.Set(ContextRoles, roles)
			c.Set(ContextRegion, region)

			return next(c)
		}
	}
}

// ModuleClearanceMiddleware validates permission based on hierarchical permissions
func ModuleClearanceMiddleware(database *db.Database, requiredPermission string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rolesStr, ok := c.Get(ContextRoles).(string)
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "Missing or invalid roles in context")
			}

			rolesList := strings.Split(rolesStr, ",")
			hasAccess := false

			tenantID, _ := c.Get(ContextTenantID).(string)
			if tenantID == "" {
				tenantID = "tenant_safari"
			}

			// Check current role or parent inherited roles
			for _, roleName := range rolesList {
				if database.CheckPermission(tenantID, strings.TrimSpace(roleName), requiredPermission) {
					hasAccess = true
					break
				}
			}

			if !hasAccess {
				return echo.NewHTTPError(http.StatusForbidden, "Access denied: Missing permissions ("+requiredPermission+")")
			}

			return next(c)
		}
	}
}

// AnyModuleClearanceMiddleware authorizes a route when the caller holds at least
// one of the supplied permissions. Useful for shared governance endpoints where
// different operational modules use the same workflow control plane.
func AnyModuleClearanceMiddleware(database *db.Database, requiredPermissions ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rolesStr, ok := c.Get(ContextRoles).(string)
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "Missing or invalid roles in context")
			}
			tenantID, _ := c.Get(ContextTenantID).(string)
			for _, roleName := range strings.Split(rolesStr, ",") {
				for _, permission := range requiredPermissions {
					if database.CheckPermission(tenantID, strings.TrimSpace(roleName), permission) {
						return next(c)
					}
				}
			}
			return echo.NewHTTPError(http.StatusForbidden, "Access denied: governance permission required")
		}
	}
}
