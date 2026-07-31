package types

import "time"

// Context NATS Header Constants
const (
	HeaderUserID    = "X-User-Id"
	HeaderUserRoles = "X-User-Roles"
	HeaderUserRegion = "X-User-Region"
	HeaderTraceID   = "X-Trace-Id"
)

// CloudEvent standard payload structure as requested
type CloudEvent[T any] struct {
	SpecVersion     string    `json:"specversion"`
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	Type            string    `json:"type"`
	Time            time.Time `json:"time"`
	DataContentType string    `json:"datacontenttype"`
	Data            T         `json:"data"`
}

// OrderCommand represents the payload for creating/approving an order
type OrderCommand struct {
	OrderID    string  `json:"order_id"`
	CustomerID string  `json:"customer_id,omitempty"`
	Amount     float64 `json:"amount,omitempty"`
}

// AuditAlert represents a security violation event
type AuditAlert struct {
	UserID    string `json:"user_id"`
	OrderID   string `json:"order_id"`
	Reason    string `json:"reason"`
	Violation string `json:"violation"`
}
