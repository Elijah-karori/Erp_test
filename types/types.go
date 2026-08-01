package types

import "time"

// Context NATS Header Constants
const (
	HeaderTenantID   = "X-Tenant-Id"
	HeaderUserID     = "X-User-Id"
	HeaderUserRoles  = "X-User-Roles"
	HeaderUserRegion = "X-User-Region"
	HeaderTraceID    = "X-Trace-Id"
)

// ERP Tenant Model
type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User role definition
type Role struct {
	Name        string   `json:"name"`          // e.g. "admin", "manager", "technician"
	ParentRole  string   `json:"parent_role"`   // Role inheritance hierarchy (e.g. "manager" parent of "technician")
	Permissions []string `json:"permissions"`   // Module access permissions (e.g. "inventory:read", "finance:write", "tasks:approve")
}

// User account details with password credentials support
type User struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	RoleName string `json:"role_name"`
	Region   string `json:"region"`
	Password string `json:"password"` // Encrypted or plain in mock
}

// Customer details
type Customer struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	DeviceID     string `json:"device_id"`     // Connected serial device
	InvoiceID    string `json:"invoice_id"`    // Connected invoice
	DispatchStatus string `json:"dispatch_status"` // e.g. "Pending", "Dispatched", "Delivered"
}

// Material request workflow (Routers, materials)
type MaterialRequest struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	TaskID        string    `json:"task_id"`
	RequesterID   string    `json:"requester_id"` // Technician ID
	ItemName      string    `json:"item_name"`    // e.g. "GPON ONU Router"
	Status        string    `json:"status"`       // e.g. "Started", "Pending_Leader_Approval", "Fulfilled", "Procuring"
	AllocatedSN   string    `json:"allocated_sn"` // Serial Number once fulfilled
	Timestamp     time.Time `json:"timestamp"`
}

// Procurement backup order
type ProcurementOrder struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	RequestID    string    `json:"request_id"`
	ItemName     string    `json:"item_name"`
	ExpectedTime time.Time `json:"expected_time"`
	Status       string    `json:"status"` // e.g. "Bidding", "Completed"
}

// Inventory item with serial number tracking
type InventoryItem struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Name         string    `json:"name"`
	SerialNumber string    `json:"serial_number"` // Tightly controlled serial tracking
	Status       string    `json:"status"`        // e.g. "In_Stock", "Assigned", "Deployed"
	AssignedTo   string    `json:"assigned_to"`   // Technician User ID
	Region       string    `json:"region"`
}

// M-Pesa Payment Methods
const (
	PaymentMethodMpesaPaybill = "Mpesa_Paybill"
	PaymentMethodMpesaTill    = "Mpesa_Till"
	PaymentMethodMpesaSTK     = "Mpesa_STK"
	PaymentMethodBankTransfer = "Bank_Transfer"
	PaymentMethodCash         = "Cash"
)

// Finance Invoice supporting credit-limits & partial payments
type Invoice struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	CustomerID    string    `json:"customer_id"`
	TotalAmount   float64   `json:"total_amount"`
	PaidAmount    float64   `json:"paid_amount"`
	BalanceAmount float64   `json:"balance_amount"`
	Status        string    `json:"status"` // e.g. "Draft", "Approved", "Partially_Paid", "Paid"
	Region        string    `json:"region"`
}

// Invoice Payment
type Payment struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	InvoiceID     string    `json:"invoice_id"`
	Amount        float64   `json:"amount"`
	PaymentMethod string    `json:"payment_method"`
	Reference     string    `json:"reference"` // M-Pesa transaction code or bank ref
	Date          time.Time `json:"date"`
}

// Task model representing jobs (installation, repair, etc)
type Task struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	Title      string    `json:"title"`
	AssignedTo string    `json:"assigned_to"` // Technician ID
	CreatedBy  string    `json:"created_by"`  // Manager ID
	Status     string    `json:"status"`      // e.g. "Pending", "In_Progress", "Completed", "Approved"
	Region     string    `json:"region"`
}

// Timesheet submission
type Timesheet struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	TaskID    string    `json:"task_id"`
	UserID    string    `json:"user_id"`
	Hours     float64   `json:"hours"`
	Date      time.Time `json:"date"`
	Status    string    `json:"status"` // e.g. "Submitted", "Approved", "Rejected"
	ApprovedBy string    `json:"approved_by"`
}

// Commands & Events for NATS
type CreateItemCommand struct {
	Name         string `json:"name"`
	SerialNumber string `json:"serial_number"`
}

// Allocate task or serial device
type AssignDeviceCommand struct {
	ItemID string `json:"item_id"`
	UserID string `json:"user_id"`
}

type RecordPaymentCommand struct {
	InvoiceID     string  `json:"invoice_id"`
	Amount        float64 `json:"amount"`
	PaymentMethod string  `json:"payment_method"`
	Reference     string  `json:"reference"`
}

type ApproveTimesheetCommand struct {
	TimesheetID string `json:"timesheet_id"`
}

type ResetPasswordCommand struct {
	UserID      string `json:"user_id"`
	NewPassword string `json:"new_password"`
}

type SubmitMaterialRequestCommand struct {
	TaskID   string `json:"task_id"`
	ItemName string `json:"item_name"`
}

type ApproveMaterialCommand struct {
	RequestID string `json:"request_id"`
}

type CreateCustomerCommand struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
}

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
