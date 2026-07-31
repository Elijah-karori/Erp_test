package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

const (
	ContextUserID   = "user_id"
	ContextRoles    = "roles"
	ContextRegion   = "region"
)

// MockAuthMiddleware extracts user credentials from request headers and sets them in Context
func MockAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userID := c.Request().Header.Get("Authorization-User-Id")
			roles := c.Request().Header.Get("Authorization-Roles")
			region := c.Request().Header.Get("Authorization-Region")

			if userID == "" {
				// Fallback to defaults if headers are empty
				userID = "anonymous"
			}
			if roles == "" {
				roles = "guest"
			}
			if region == "" {
				region = "unknown"
			}

			c.Set(ContextUserID, userID)
			c.Set(ContextRoles, roles)
			c.Set(ContextRegion, region)

			return next(c)
		}
	}
}

// EdgeRBACMiddleware guards specific endpoints with role-based checks
func EdgeRBACMiddleware(allowedRoles ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rolesStr, ok := c.Get(ContextRoles).(string)
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "Missing or invalid roles in context")
			}

			rolesList := strings.Split(rolesStr, ",")
			allowed := false

			for _, allowedRole := range allowedRoles {
				for _, userRole := range rolesList {
					if strings.TrimSpace(userRole) == allowedRole {
						allowed = true
						break
					}
				}
				if allowed {
					break
				}
			}

			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "Missing required roles for this operation")
			}

			return next(c)
		}
	}
}
