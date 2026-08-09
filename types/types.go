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
	Name        string   `json:"name"`        // e.g. "admin", "manager", "technician"
	ParentRole  string   `json:"parent_role"` // Role inheritance hierarchy (e.g. "manager" parent of "technician")
	Permissions []string `json:"permissions"` // Module access permissions (e.g. "inventory:read", "finance:write", "tasks:approve")
}

// User account details with password credentials support
type User struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Email        string `json:"email"` // Unique login identifier
	RoleName     string `json:"role_name"`
	Region       string `json:"region"`
	PasswordHash string `json:"-"` // bcrypt hash, never serialized in API responses
	ManagerID    string `json:"manager_id,omitempty"`
}

// Workspace Invitation
type Invitation struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	RoleName  string    `json:"role_name"`
	Region    string    `json:"region"`
	ManagerID string    `json:"manager_id,omitempty"`
	Token     string    `json:"token"`
	Status    string    `json:"status"` // 'Pending', 'Accepted', 'Expired'
	CreatedAt time.Time `json:"created_at"`
}

// Customer details
type Customer struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	Email          string `json:"email"`
	DeviceID       string `json:"device_id"`       // Connected serial device
	InvoiceID      string `json:"invoice_id"`      // Connected invoice
	DispatchStatus string `json:"dispatch_status"` // e.g. "Pending", "Dispatched", "Delivered"
}

// Material request workflow (Routers, materials)
type MaterialRequest struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TaskID      string    `json:"task_id"`
	RequesterID string    `json:"requester_id"` // Technician ID
	ItemName    string    `json:"item_name"`    // e.g. "GPON ONU Router"
	Status      string    `json:"status"`       // e.g. "Started", "Pending_Leader_Approval", "Fulfilled", "Procuring"
	AllocatedSN string    `json:"allocated_sn"` // Serial Number once fulfilled
	Timestamp   time.Time `json:"timestamp"`
}

// Procurement backup order
type ProcurementOrder struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	RequestID       string    `json:"request_id"`
	ItemName        string    `json:"item_name"`
	ExpectedTime    time.Time `json:"expected_time"`
	Status          string    `json:"status"` // e.g. "Bidding", "Completed"
	BarcodePhotoURL string    `json:"barcode_photo_url,omitempty"`
	ConfirmedAt     time.Time `json:"confirmed_at,omitempty"`
	ConfirmedBy     string    `json:"confirmed_by,omitempty"`
}

// Invoice Note for serialized item dispatches
type InvoiceNote struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	RequestID   string    `json:"request_id"`
	ItemName    string    `json:"item_name"`
	AllocatedSN string    `json:"allocated_sn"`
	TaskID      string    `json:"task_id"`
	RequesterID string    `json:"requester_id"`
	UsageType   string    `json:"usage_type"` // 'Internal', 'Customer_Installation', 'Customer_Broken'
	Status      string    `json:"status"`     // 'Paid_Usage_Support', 'Pending_Payment', 'Cleared_Paid', 'Reconciliation_Started'
	InvoiceID   string    `json:"invoice_id,omitempty"`
	PaymentID   string    `json:"payment_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Inventory item with serial number tracking
type InventoryItem struct {
	ID               string `json:"id"`
	TenantID         string `json:"tenant_id"`
	Name             string `json:"name"`
	SerialNumber     string `json:"serial_number"` // Tightly controlled serial tracking
	Status           string `json:"status"`        // e.g. "In_Stock", "Assigned", "Deployed"
	AssignedTo       string `json:"assigned_to"`   // Technician User ID
	Region           string `json:"region"`
	ReorderThreshold int    `json:"reorder_threshold"`
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
	ID            string  `json:"id"`
	TenantID      string  `json:"tenant_id"`
	CustomerID    string  `json:"customer_id"`
	TotalAmount   float64 `json:"total_amount"`
	PaidAmount    float64 `json:"paid_amount"`
	BalanceAmount float64 `json:"balance_amount"`
	Status        string  `json:"status"` // e.g. "Draft", "Approved", "Partially_Paid", "Paid"
	Region        string  `json:"region"`
}

// Invoice Payment
type BusinessLedgerEntry struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	TransactionID string    `json:"transaction_id"`
	TaskID        string    `json:"task_id,omitempty"`
	InvoiceID     string    `json:"invoice_id,omitempty"`
	EntityType    string    `json:"entity_type"`
	EntityID      string    `json:"entity_id"`
	Account       string    `json:"account"`
	EntryType     string    `json:"entry_type"`
	Amount        float64   `json:"amount"`
	Quantity      float64   `json:"quantity"`
	UnitCost      float64   `json:"unit_cost"`
	Currency      string    `json:"currency"`
	Reference     string    `json:"reference,omitempty"`
	ActorID       string    `json:"actor_id"`
	CreatedAt     time.Time `json:"created_at"`
	ReversalOf    string    `json:"reversal_of,omitempty"`
}

type ProfitabilitySummary struct {
	TaskID       string  `json:"task_id"`
	TaskTitle    string  `json:"task_title"`
	Revenue      float64 `json:"revenue"`
	MaterialCost float64 `json:"material_cost"`
	LaborCost    float64 `json:"labor_cost"`
	GrossMargin  float64 `json:"gross_margin"`
	MarginPct    float64 `json:"margin_pct"`
}

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
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Title      string     `json:"title"`
	AssignedTo string     `json:"assigned_to"` // Technician ID
	CreatedBy  string     `json:"created_by"`  // Manager ID
	Status     string     `json:"status"`      // e.g. "Pending", "In_Progress", "Completed", "Approved"
	Region     string     `json:"region"`
	DueDate    *time.Time `json:"due_date,omitempty"`
	DependsOn  string     `json:"depends_on,omitempty"`
	CustomerID string     `json:"customer_id,omitempty"`
	ProjectID  string     `json:"project_id,omitempty"`
}

// Timesheet submission
type Timesheet struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	TaskID     string    `json:"task_id"`
	UserID     string    `json:"user_id"`
	Hours      float64   `json:"hours"`
	Date       time.Time `json:"date"`
	Status     string    `json:"status"` // e.g. "Submitted", "Approved", "Rejected"
	ApprovedBy string    `json:"approved_by"`
}

// Commands & Events for NATS
type CreateItemCommand struct {
	Name             string `json:"name"`
	SerialNumber     string `json:"serial_number"`
	ReorderThreshold int    `json:"reorder_threshold"`
}

type CreateTaskCommand struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	AssignedTo string     `json:"assigned_to"`
	DueDate    *time.Time `json:"due_date,omitempty"`
	DependsOn  string     `json:"depends_on,omitempty"`
}

type UpdateTaskStatusCommand struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
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
	RequestID   string `json:"request_id,omitempty"`
	TaskID      string `json:"task_id"`
	ItemName    string `json:"item_name"`
	RequesterID string `json:"requester_id,omitempty"`
}

type ApproveMaterialCommand struct {
	RequestID string `json:"request_id"`
}

// FulfillMaterialRequestCommand is released only after the governance workflow
// reaches EXECUTED. The consumer performs the inventory/procurement transaction
// atomically and idempotently.
type FulfillMaterialRequestCommand struct {
	RequestID   string `json:"request_id"`
	TaskID      string `json:"task_id"`
	ItemName    string `json:"item_name"`
	RequesterID string `json:"requester_id"`
}

// Customer/Project lifecycle models unify CRM, commercial, delivery and CX.
type Lead struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Email     string    `json:"email"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	OwnerID   string    `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
}

type Quote struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	CustomerID string     `json:"customer_id"`
	LeadID     string     `json:"lead_id,omitempty"`
	Title      string     `json:"title"`
	Amount     float64    `json:"amount"`
	Status     string     `json:"status"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	CreatedBy  string     `json:"created_by"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Project struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	CustomerID string     `json:"customer_id"`
	QuoteID    string     `json:"quote_id,omitempty"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Region     string     `json:"region"`
	StartDate  *time.Time `json:"start_date,omitempty"`
	TargetDate *time.Time `json:"target_date,omitempty"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ProjectEvidence struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	ProjectID    string    `json:"project_id"`
	TaskID       string    `json:"task_id,omitempty"`
	EvidenceType string    `json:"evidence_type"`
	URL          string    `json:"url"`
	Note         string    `json:"note,omitempty"`
	CapturedBy   string    `json:"captured_by"`
	CreatedAt    time.Time `json:"created_at"`
}

type CustomerSatisfaction struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	CustomerID string    `json:"customer_id"`
	ProjectID  string    `json:"project_id,omitempty"`
	Rating     int       `json:"rating"`
	Comment    string    `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateLeadPayload struct {
	Name   string `json:"name"`
	Phone  string `json:"phone"`
	Email  string `json:"email"`
	Source string `json:"source"`
}
type CreateQuotePayload struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	LeadID     string     `json:"lead_id"`
	Title      string     `json:"title"`
	Amount     float64    `json:"amount"`
	ValidUntil *time.Time `json:"valid_until"`
}
type CreateProjectPayload struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	QuoteID    string     `json:"quote_id"`
	Name       string     `json:"name"`
	Region     string     `json:"region"`
	StartDate  *time.Time `json:"start_date"`
	TargetDate *time.Time `json:"target_date"`
}
type CreateEvidencePayload struct {
	ProjectID    string `json:"project_id"`
	TaskID       string `json:"task_id"`
	EvidenceType string `json:"evidence_type"`
	URL          string `json:"url"`
	Note         string `json:"note"`
}
type CreateSatisfactionPayload struct {
	CustomerID string `json:"customer_id"`
	ProjectID  string `json:"project_id"`
	Comment    string `json:"comment"`
	Rating     int    `json:"rating"`
}

type ApproveQuoteCommand struct {
	QuoteID string `json:"quote_id"`
}

type CreateCustomerCommand struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
}

type UpdateInventoryThresholdCommand struct {
	ItemID           string `json:"item_id"`
	ReorderThreshold int    `json:"reorder_threshold"`
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

// SupportTicket model representing customer issues/tickets
type SupportTicket struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	CustomerID  string    `json:"customer_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"` // 'Open', 'Converted', 'Closed'
	TaskID      string    `json:"task_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreateSupportTicketCommand struct {
	CustomerID  string `json:"customer_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type ConvertTicketToTaskCommand struct {
	TicketID   string `json:"ticket_id"`
	Title      string `json:"title"`
	AssignedTo string `json:"assigned_to"`
	DependsOn  string `json:"depends_on,omitempty"`
}

// Workflow approval instance — immutable governance record for maker-checker execution.
type WorkflowInstance struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	EntityType   string    `json:"entity_type"`
	EntityID     string    `json:"entity_id"`
	WorkflowKey  string    `json:"workflow_key"`
	Step         int       `json:"step"`
	Status       string    `json:"status"`
	RequestedBy  string    `json:"requested_by"`
	CheckedBy    string    `json:"checked_by,omitempty"`
	ExecutedBy   string    `json:"executed_by,omitempty"`
	ReconciledBy string    `json:"reconciled_by,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type WorkflowDecision struct {
	InstanceID string `json:"instance_id"`
	Action     string `json:"action"` // approve, reject, return, execute, reconcile
	Reason     string `json:"reason,omitempty"`
}
