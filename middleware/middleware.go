package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

import (
	"erp-event-bus/db"
)

const (
	ContextTenantID = "tenant_id"
	ContextUserID   = "user_id"
	ContextRoles    = "roles"
	ContextRegion   = "region"
)

// MockAuthMiddleware extracts multi-tenant and user credentials from request headers and sets them in Context
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

			// Check current role or parent inherited roles
			for _, roleName := range rolesList {
				if database.CheckPermission(strings.TrimSpace(roleName), requiredPermission) {
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
