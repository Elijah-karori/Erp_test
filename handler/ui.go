package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"erp-event-bus/auth"
	"erp-event-bus/db"
	"erp-event-bus/email"
	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

type UIHandler struct {
	db       *db.Database
	sqliteDB *db.SQLiteDB
	emailSvc *email.EmailService
}

func NewUIHandler(database *db.Database, sdb *db.SQLiteDB, emailSvc *email.EmailService) *UIHandler {
	return &UIHandler{db: database, sqliteDB: sdb, emailSvc: emailSvc}
}

type InvitePayload struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	RoleName  string `json:"role_name"`
	Region    string `json:"region"`
	ManagerID string `json:"manager_id"`
}

type ActivatePayload struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type UpdateManagerPayload struct {
	UserID    string `json:"user_id"`
	ManagerID string `json:"manager_id"`
}

type UpdateRolePayload struct {
	UserID   string `json:"user_id"`
	RoleName string `json:"role_name"`
}

type UpdateUserRolePayload struct {
	UserID   string `json:"user_id"`
	RoleName string `json:"role_name"`
}

type CreateSupportTicketPayload struct {
	CustomerID  string `json:"customer_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type ManualInventoryItemPayload struct {
	Name             string `json:"name"`
	SerialNumber     string `json:"serial_number"`
	ReorderThreshold int    `json:"reorder_threshold"`
	Region           string `json:"region"`
}

type UpdateInvoiceNotePayload struct {
	NoteID     string `json:"note_id"`
	UsageType  string `json:"usage_type"` // 'Internal', 'Customer_Installation', 'Customer_Broken'
	CustomerID string `json:"customer_id,omitempty"`
}

type ConfirmProcurementPayload struct {
	ProcID          string `json:"proc_id"`
	BarcodePhotoURL string `json:"barcode_photo_url"`
}

type ConvertTicketToTaskPayload struct {
	TicketID   string `json:"ticket_id"`
	Title      string `json:"title"`
	AssignedTo string `json:"assigned_to"`
	DependsOn  string `json:"depends_on"`
}

type UpdateRBACPayload struct {
	RoleName    string   `json:"role_name"`
	Permissions []string `json:"permissions"`
}

type CreateTenantPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CreateUserPayload struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	RoleName string `json:"role_name"`
	Region   string `json:"region"`
}

// ServeDashboard returns the gorgeous fully-functional Tailwind UI
func (h *UIHandler) ServeDashboard(c echo.Context) error {
	return c.HTML(http.StatusOK, htmlContent)
}

// CreateTenantHandler creates a new tenant subscriber dynamically
func (h *UIHandler) CreateTenant(c echo.Context) error {
	var payload CreateTenantPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.ID == "" || payload.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Tenant ID and Name are required"})
	}

	h.db.CreateTenant(payload.ID, payload.Name)
	h.sqliteDB.Log(payload.ID, "SYSTEM", "CreateTenant_Success", fmt.Sprintf("Tenant subscriber '%s' registered dynamically", payload.Name))

	return c.JSON(http.StatusOK, map[string]string{"message": "Tenant registered successfully"})
}

// CreateUserHandler lets an authenticated tenant_admin add a teammate.
// Gated behind the users:* permission in main.go — trusts the caller's
// choice of role, unlike the public self-service RegisterHandler.
func (h *UIHandler) CreateUser(c echo.Context) error {
	var payload CreateUserPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.ID == "" || payload.TenantID == "" || payload.Name == "" || payload.Email == "" || payload.RoleName == "" || payload.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id, tenant_id, name, email, role_name, and region are all required"})
	}

	tempPassword := "TempPass123!" + uuid.NewString()[:8]
	hash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to provision credentials"})
	}

	if err := h.db.CreateUser(payload.ID, payload.TenantID, payload.Name, payload.Email, payload.RoleName, payload.Region, hash); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	h.sqliteDB.Log(payload.TenantID, "SYSTEM", "CreateUser_Success", fmt.Sprintf("User %s (%s) registered under tenant %s", payload.Name, payload.RoleName, payload.TenantID))

	// Temp password is returned exactly once and never logged or stored in
	// plaintext — the new user must reset it (or admin re-issues) if lost.
	return c.JSON(http.StatusOK, map[string]string{
		"message":       "User registered successfully",
		"temp_password": tempPassword,
	})
}

// InviteUser creates a workspace invitation and dispatches an activation email
func (h *UIHandler) InviteUser(c echo.Context) error {
	var p InvitePayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	if p.Email == "" || p.Name == "" || p.RoleName == "" || p.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email, name, role_name, and region are required"})
	}

	token := uuid.NewString()
	inv := &types.Invitation{
		ID:        "inv_" + uuid.NewString()[:12],
		TenantID:  tenantID,
		Email:     p.Email,
		Name:      p.Name,
		RoleName:  p.RoleName,
		Region:    p.Region,
		ManagerID: p.ManagerID,
		Token:     token,
		Status:    "Pending",
		CreatedAt: time.Now(),
	}

	if err := h.db.CreateInvitation(inv); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}

	// Construct activation URL
	activationUrl := fmt.Sprintf("http://localhost:8080/activate.html?token=%s&tenant_id=%s&role_assignment_id=%s", token, tenantID, p.RoleName)

	// Send Invite Email
	if err := h.emailSvc.SendInviteEmail(p.Email, p.Name, activationUrl, tenantID, p.RoleName); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to dispatch invite email"})
	}

	userID, _ := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "InviteUser_Success", fmt.Sprintf("Sent workspace invitation to %s with token", p.Email))

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Invitation sent successfully",
		"token":   token,
		"url":     activationUrl,
	})
}

// PreviewInvitation lets the public activation page load invitation metadata safely
func (h *UIHandler) PreviewInvitation(c echo.Context) error {
	token := c.QueryParam("token")
	if token == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "token is required"})
	}

	inv, err := h.db.GetInvitationByToken(token)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "invitation not found"})
	}

	return c.JSON(http.StatusOK, inv)
}

// ActivateUser completes the registration process of an invited teammate
func (h *UIHandler) ActivateUser(c echo.Context) error {
	var p ActivatePayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	if p.Token == "" || p.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "token and password are required"})
	}

	hash, err := auth.HashPassword(p.Password)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	user, err := h.db.AcceptInvitation(p.Token, hash)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	// Generate immediate authentication token to log them in automatically
	token, err := auth.GenerateToken(user)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate JWT session"})
	}

	h.sqliteDB.Log(user.TenantID, user.ID, "ActivateUser_Success", fmt.Sprintf("Teammate %s activated their workspace account", user.Email))

	// Send Login confirmation too
	_ = h.emailSvc.SendLoginConfirmation(user.Email, user.Name, time.Now().Format(time.RFC1123), user.Region)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"token":     token,
		"user_id":   user.ID,
		"tenant_id": user.TenantID,
		"name":      user.Name,
		"email":     user.Email,
		"role_name": user.RoleName,
		"region":    user.Region,
	})
}

// UpdateUserManager updates the hierarchy reporting relationship
func (h *UIHandler) UpdateUserManager(c echo.Context) error {
	var p UpdateManagerPayload
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if p.UserID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "user_id is required"})
	}

	if err := h.db.UpdateUserManager(p.UserID, p.ManagerID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	operatorID, _ := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, operatorID, "UpdateUserManager_Success", fmt.Sprintf("Reporting manager of user %s set to %s", p.UserID, p.ManagerID))

	return c.JSON(http.StatusOK, map[string]string{"message": "Reporting relationship updated successfully"})
}

// UpdateUserRole updates the teammate role assignment
func (h *UIHandler) UpdateUserRole(c echo.Context) error {
	var p UpdateUserRolePayload // using the corrected struct name
	if err := c.Bind(&p); err != nil {
		// Try using payload name
		var p2 UpdateRolePayload
		if err2 := c.Bind(&p2); err2 == nil && p2.UserID != "" {
			p.UserID = p2.UserID
			p.RoleName = p2.RoleName
		} else {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}

	if p.UserID == "" || p.RoleName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "user_id and role_name are required"})
	}

	if err := h.db.UpdateUserRole(p.UserID, p.RoleName); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	operatorID, _ := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, operatorID, "UpdateUserRole_Success", fmt.Sprintf("Role of user %s updated to %s", p.UserID, p.RoleName))

	return c.JSON(http.StatusOK, map[string]string{"message": "Role assigned successfully"})
}

func (h *UIHandler) CreateSupportTicket(c echo.Context) error {
	var payload CreateSupportTicketPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.CustomerID == "" || payload.Title == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id and title are required"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	ticket := &types.SupportTicket{
		ID:          "ticket_" + uuid.NewString()[:8],
		TenantID:    tenantID,
		CustomerID:  payload.CustomerID,
		Title:       payload.Title,
		Description: payload.Description,
		Status:      "Open",
		CreatedAt:   time.Now(),
	}

	if err := h.db.CreateSupportTicket(ticket); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.sqliteDB.Log(tenantID, userID, "CreateTicket_Success", fmt.Sprintf("Support ticket %s created for customer %s", ticket.ID, ticket.CustomerID))

	return c.JSON(http.StatusOK, ticket)
}

func (h *UIHandler) ConvertTicketToTask(c echo.Context) error {
	var payload ConvertTicketToTaskPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.TicketID == "" || payload.Title == "" || payload.AssignedTo == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "ticket_id, title, and assigned_to are required"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	ticket, err := h.db.GetSupportTicket(payload.TicketID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "support ticket not found"})
	}

	if ticket.TenantID != tenantID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "unauthorized: tenant mismatch"})
	}

	taskID := "task_" + uuid.NewString()[:8]
	now := time.Now().Add(5 * 24 * time.Hour) // default due date is 5 days from now
	task := &types.Task{
		ID:         taskID,
		TenantID:   tenantID,
		Title:      payload.Title,
		AssignedTo: payload.AssignedTo,
		CreatedBy:  userID,
		Status:     "Pending",
	}

	// Fetch assignee details to set task region
	assigneeUser, errUser := h.db.GetUser(payload.AssignedTo)
	if errUser == nil && assigneeUser != nil {
		task.Region = assigneeUser.Region
	} else {
		task.Region = "Nairobi" // fallback
	}
	task.DueDate = &now
	task.DependsOn = payload.DependsOn

	h.db.SaveTask(task)

	if err := h.db.ConvertTicketToTaskTransaction(payload.TicketID, tenantID, taskID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.sqliteDB.Log(tenantID, userID, "ConvertTicket_Success", fmt.Sprintf("Converted ticket %s to task %s assigned to %s", payload.TicketID, taskID, payload.AssignedTo))

	// Send Email alerts to the manager/leader!
	if h.emailSvc != nil {
		// Look up the assignee's manager/leader to alert them
		if assigneeUser != nil && assigneeUser.ManagerID != "" {
			managerUser, errMgr := h.db.GetUser(assigneeUser.ManagerID)
			if errMgr == nil && managerUser != nil {
				subject := fmt.Sprintf("ERP Alert: New Task Assignment for Subordinate (%s)", assigneeUser.Name)
				body := fmt.Sprintf(`<h2>New Task Assignment Alert</h2>
<p>Hello %s,</p>
<p>A new customer support ticket has been converted into a task and assigned to your team member <strong>%s</strong>:</p>
<ul>
  <li><strong>Task ID:</strong> %s</li>
  <li><strong>Task Title:</strong> %s</li>
  <li><strong>Converted From Ticket:</strong> %s</li>
</ul>
<p>Please check your team tracking dashboard to supervise progress.</p>
<p>Best regards,<br>ERP Support System</p>`, managerUser.Name, assigneeUser.Name, taskID, payload.Title, payload.TicketID)
				_ = h.emailSvc.SendEmail(managerUser.Email, subject, body, true)
			}
		}

		// Also alert the assignee
		if assigneeUser != nil {
			subject := "ERP Alert: You have been assigned a new task"
			body := fmt.Sprintf(`<h2>New Task Assigned</h2>
<p>Hello %s,</p>
<p>A new task has been assigned to you:</p>
<ul>
  <li><strong>Task ID:</strong> %s</li>
  <li><strong>Task Title:</strong> %s</li>
  <li><strong>Due Date:</strong> %s</li>
</ul>
<p>Please log in to your technician dashboard to start and execute this task.</p>
<p>Best regards,<br>ERP Support System</p>`, assigneeUser.Name, taskID, payload.Title, now.Format("2006-01-02"))
			_ = h.emailSvc.SendEmail(assigneeUser.Email, subject, body, true)
		}
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "Ticket converted to task successfully", "task_id": taskID})
}

func (h *UIHandler) AddManualInventoryItem(c echo.Context) error {
	var payload ManualInventoryItemPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.Name == "" || payload.SerialNumber == "" || payload.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name, serial_number, and region are required"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	if err := h.db.SaveInventoryItemDirect(tenantID, payload.Name, payload.SerialNumber, payload.ReorderThreshold, payload.Region); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.sqliteDB.Log(tenantID, userID, "ManualInventory_Add", fmt.Sprintf("Manually added item %s (SN: %s)", payload.Name, payload.SerialNumber))

	return c.JSON(http.StatusOK, map[string]string{"message": "Item successfully registered in inventory"})
}

func (h *UIHandler) UpdateInvoiceNote(c echo.Context) error {
	var payload UpdateInvoiceNotePayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.NoteID == "" || payload.UsageType == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "note_id and usage_type are required"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	if err := h.db.UpdateInvoiceNoteUsageType(payload.NoteID, tenantID, payload.UsageType, payload.CustomerID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.sqliteDB.Log(tenantID, userID, "InvoiceNote_Update", fmt.Sprintf("Updated invoice note %s usage to %s", payload.NoteID, payload.UsageType))

	// If Customer_Installation or Customer_Broken, send email to requester
	if payload.UsageType != "Internal" {
		subject := "ERP Alert: Customer Invoice Note Generated (Pending Payment)"
		body := fmt.Sprintf(`<h2>Requisition Invoice Note</h2>
<p>An installation/repair usage was logged for serial allocation. A draft invoice of <strong>KSh 12,500.00</strong> has been dispatched for payment confirmation.</p>
<p>Please attach transaction receipts once completed to clear reconciliation holds on tasks.</p>
<p>Best regards,<br>ERP Billing Desk</p>`)

		user, err := h.db.GetUser(userID)
		if err == nil && user != nil {
			_ = h.emailSvc.SendEmail(user.Email, subject, body, true)
		}
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "Invoice note successfully updated"})
}

func (h *UIHandler) ConfirmProcurementReceipt(c echo.Context) error {
	var payload ConfirmProcurementPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.ProcID == "" || payload.BarcodePhotoURL == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "proc_id and barcode_photo_url are required"})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	user, _ := h.db.GetUser(userID)
	userName := "System_Admin"
	if user != nil {
		userName = user.Name
	}

	if err := h.db.ConfirmProcurementReceipt(payload.ProcID, tenantID, userName, payload.BarcodePhotoURL); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.sqliteDB.Log(tenantID, userID, "Procurement_Confirm", fmt.Sprintf("Confirmed procurement receipt %s, processed barcode %s", payload.ProcID, payload.BarcodePhotoURL))

	return c.JSON(http.StatusOK, map[string]string{"message": "Purchase receipt confirmed. Serialized assets added automatically after barcode verification."})
}

// ServeActivationPage returns the Tailwind-styled activation page
func (h *UIHandler) ServeActivationPage(c echo.Context) error {
	return c.HTML(http.StatusOK, activationHtmlContent)
}

// GetState returns current in-memory DB state for UI rendering, scoped to
// the caller's own tenant — previously this returned every tenant's users,
// invoices, and inventory to any authenticated caller regardless of role.
func (h *UIHandler) GetState(c echo.Context) error {
	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	userID, _ := c.Get(middleware.ContextUserID).(string)
	roleName, _ := c.Get(middleware.ContextRoles).(string)
	state := h.db.GetStateForTenant(tenantID, userID, roleName)
	// Roles are global permission definitions, not tenant data, so it's safe
	// to include them unfiltered — the UI needs the full set to render the
	// RBAC policy grid.
	state["roles"] = h.db.GetRolesForTenant(tenantID)
	return c.JSON(http.StatusOK, state)
}

// UpdateRBAC handles interactive RBAC/ABAC permission toggling from UI
func (h *UIHandler) UpdateRBAC(c echo.Context) error {
	var payload UpdateRBACPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	h.db.UpdateRolePermissions(tenantID, payload.RoleName, payload.Permissions)
	userID, _ := c.Get(middleware.ContextUserID).(string)

	h.sqliteDB.Log(tenantID, userID, "UpdateRBAC_Success", fmt.Sprintf("Role '%s' permissions updated dynamically to %v", payload.RoleName, payload.Permissions))

	return c.JSON(http.StatusOK, map[string]string{"message": "Permissions updated successfully"})
}

// GetLogs returns live SQLite audit logs, scoped to the caller's own
// tenant — this used to return the full cross-tenant audit trail to any
// authenticated caller.
func (h *UIHandler) GetLogs(c echo.Context) error {
	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	logs, err := h.sqliteDB.GetLogsForTenant(tenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, logs)
}

// ExportLogsExcel downloads the audit sheet for the caller's own tenant.
func (h *UIHandler) ExportLogsExcel(c echo.Context) error {
	tenantID, _ := c.Get(middleware.ContextTenantID).(string)
	logs, err := h.sqliteDB.GetLogsForTenant(tenantID)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	buf, err := db.GenerateAuditExcel(logs)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	c.Response().Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=erp_audit_log.xlsx")
	return c.Blob(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf)
}

const htmlContent = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SME Kenya ERP Event Bus Console</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <link href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.0.0/css/all.min.css" rel="stylesheet">
    <script>
        tailwind.config = {
            theme: {
                extend: {
                    colors: {
                        brand: {
                            50: '#f5f7f6',
                            100: '#e1e7e4',
                            500: '#2f4f4f',
                            600: '#243e3e',
                            900: '#142222',
                        }
                    }
                }
            }
        }
    </script>
</head>
<body class="bg-brand-900 text-slate-100 min-h-screen font-sans overflow-x-hidden">

    <!-- LOGIN / AUTHENTICATION GATE SCREEN -->
    <div id="loginGate" class="min-h-screen flex items-center justify-center p-4 bg-brand-900">
        <div class="max-w-md w-full bg-brand-500 rounded-2xl border border-brand-600 p-6 md:p-8 shadow-2xl space-y-6">
            <div class="text-center space-y-2">
                <i class="fa-solid fa-network-wired text-emerald-400 text-5xl"></i>
                <h2 class="text-2xl font-bold tracking-wide text-white">SME Kenya ERP Login</h2>
                <p class="text-sm text-slate-300">Sign in with your workspace email, or register below</p>
            </div>

            <form onsubmit="handleLogin(event)" class="space-y-4">
                <div>
                    <label class="block text-xs font-bold text-slate-400 mb-1">Email</label>
                    <input type="email" id="loginEmail" placeholder="alice@safari.test" required autocomplete="username" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                </div>
                <div>
                    <label class="block text-xs font-bold text-slate-400 mb-1">Tenant Space ID (Optional)</label>
                    <input type="text" id="loginTenantID" placeholder="tenant_safari" autocomplete="organization" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                </div>
                <div>
                    <label class="block text-xs font-bold text-slate-400 mb-1">Password</label>
                    <input type="password" id="loginPassword" placeholder="••••••••" required autocomplete="current-password" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                </div>
                <div id="loginError" class="hidden text-xs text-rose-400 font-semibold"></div>

                <button type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white font-bold py-3 rounded-lg transition duration-200 shadow-md">
                    Enter Workspace
                </button>
            </form>

            <div class="border-t border-brand-600 pt-4 text-center text-xs font-semibold text-emerald-400">
                <button onclick="openRegisterModal()" class="hover:underline">Create an account</button>
            </div>
        </div>
    </div>

    <!-- MAIN CO-LOCATED ERP DASHBOARD VIEW -->
    <div id="dashboardApp" class="hidden min-h-screen flex flex-col lg:flex-row">

        <!-- MOBILE NAVIGATION HEADER -->
        <header class="lg:hidden bg-brand-500 border-b border-brand-600 px-4 py-4 flex items-center justify-between shadow-md sticky top-0 z-50">
            <div class="flex items-center space-x-3">
                <i class="fa-solid fa-network-wired text-emerald-400 text-xl"></i>
                <span id="mobileTenantTitle" class="text-md font-bold tracking-wide text-white truncate max-w-[150px]">SME Kenya ERP</span>
            </div>

            <div class="flex items-center space-x-3">
                <div class="relative bg-brand-900 p-2 rounded-lg border border-brand-600 cursor-pointer" onclick="toggleNotificationPane()">
                    <i class="fa-regular fa-bell text-slate-300 text-xs"></i>
                    <span id="mobileNotifBadge" class="absolute top-0 right-0 w-2 h-2 bg-rose-500 rounded-full border border-brand-900 hidden"></span>
                </div>
                <button onclick="toggleMobileSidebar()" class="bg-brand-900 border border-brand-600 text-slate-200 p-2 rounded-lg hover:bg-brand-600 focus:outline-none">
                    <i class="fa-solid fa-bars text-lg" id="hamburgerIcon"></i>
                </button>
            </div>
        </header>

        <!-- Role-Based Responsive Sidebar Layout -->
        <aside id="sidebarDrawer" class="hidden lg:flex w-full lg:w-64 bg-brand-500 border-r border-brand-600 flex-col shadow-xl shrink-0 fixed lg:static inset-y-0 left-0 z-40 lg:z-auto transition-transform duration-300 transform lg:transform-none">

            <div class="hidden lg:flex px-6 py-5 border-b border-brand-600 items-center justify-between bg-brand-600">
                <div class="flex items-center space-x-3">
                    <i class="fa-solid fa-cubes text-emerald-400 text-2xl"></i>
                    <div>
                        <div id="sidebarTenantName" class="text-sm font-bold truncate max-w-[150px]">Safaricom ISP</div>
                        <div class="text-[10px] text-slate-400 tracking-widest uppercase">Ecosystem</div>
                    </div>
                </div>
            </div>

            <!-- Profile Summary Box -->
            <div class="p-4 mx-4 my-4 bg-brand-900 border border-brand-600 rounded-lg flex items-center space-x-3">
                <div class="w-8 h-8 rounded-full bg-brand-500 flex items-center justify-center text-emerald-300 font-bold" id="sidebarAvatar">
                    A
                </div>
                <div class="flex-1 min-w-0">
                    <div id="sidebarUserName" class="text-xs font-bold truncate text-white">Alice Admin</div>
                    <div id="sidebarRoleLabel" class="text-[9px] font-mono text-emerald-400 font-semibold truncate bg-brand-500 px-1.5 py-0.5 rounded inline-block mt-0.5">tenant_admin</div>
                </div>
            </div>

            <!-- Sidebar Navigation links -->
            <nav id="sidebarNav" class="flex-1 px-4 space-y-1.5 text-sm font-medium">
                <!-- Filled dynamically according to module permissions -->
            </nav>

            <div class="p-4 border-t border-brand-600">
                <button onclick="handleLogout()" class="w-full flex items-center justify-center space-x-2 bg-brand-900 border border-brand-600 hover:bg-rose-950 hover:text-white text-rose-400 font-bold py-2 rounded-lg transition duration-200 text-xs">
                    <i class="fa-solid fa-right-from-bracket"></i>
                    <span>Logout Account</span>
                </button>
            </div>
        </aside>

        <!-- Dynamic Module view layout panel -->
        <div class="flex-1 flex flex-col min-w-0 bg-brand-900 overflow-y-auto">

            <header class="hidden lg:flex bg-brand-500 border-b border-brand-600 px-8 py-4 items-center justify-between shadow-sm">
                <div>
                    <h2 id="currentModuleTitle" class="text-lg font-bold text-white">Dashboard Home</h2>
                    <p class="text-xs text-slate-400">Live Kenya-SME enterprise state</p>
                </div>
                <div class="flex items-center space-x-4">
                    <div class="relative bg-brand-900 p-2.5 rounded-lg border border-brand-600 cursor-pointer" onclick="toggleNotificationPane()">
                        <i class="fa-regular fa-bell text-slate-300 text-sm"></i>
                        <span id="notifBadge" class="absolute top-0 right-0 w-2.5 h-2.5 bg-rose-500 rounded-full border border-brand-900 hidden"></span>
                    </div>
                </div>
            </header>

            <!-- Notification Drawer Pane -->
            <div id="notifPane" class="hidden mx-4 lg:mx-8 mt-4 p-4 bg-brand-500 rounded-xl border border-brand-600 shadow-lg space-y-3">
                <h4 class="text-xs uppercase font-bold text-slate-400 flex items-center justify-between">
                    <span>Recent Broadcast Alerts</span>
                    <button onclick="clearNotifications()" class="text-[10px] text-rose-400 hover:underline">Clear All</button>
                </h4>
                <div id="notifList" class="space-y-2 max-h-[200px] overflow-y-auto font-mono text-[11px] text-emerald-300">
                    <div class="p-2 bg-brand-900 rounded border border-brand-600 italic text-slate-500">No recent notifications logged.</div>
                </div>
            </div>

            <!-- Workspace Panels -->
            <div class="p-4 md:p-8 max-w-6xl w-full mx-auto space-y-6">

                <!-- Module A: Dashboard View -->
                <div id="view_dashboard" class="space-y-6">
                    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Serialized ONU Inventory</span>
                                <i class="fa-solid fa-box text-emerald-400"></i>
                            </div>
                            <h3 id="stat_inventory_cnt" class="text-3xl font-bold mt-2 text-white">3</h3>
                            <p class="text-xs text-slate-300 mt-1">Serialized units stored across regions.</p>
                        </div>
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Total Invoiced Amount</span>
                                <i class="fa-solid fa-money-bill-wave text-amber-400"></i>
                            </div>
                            <h3 id="stat_finance_val" class="text-3xl font-bold mt-2 text-white">KSh 20,000</h3>
                            <p class="text-xs text-slate-300 mt-1">Total outstanding + paid balances.</p>
                        </div>
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Pending Approvals</span>
                                <i class="fa-solid fa-clock-rotate-left text-sky-400"></i>
                            </div>
                            <h3 id="stat_tasks_cnt" class="text-3xl font-bold mt-2 text-white">1</h3>
                            <p class="text-xs text-slate-300 mt-1">Subordinate timesheets awaiting manager check.</p>
                        </div>
                    </div>

                    <!-- Overdue Tasks Section (Task 4) -->
                    <div id="overdueTasksSection" class="bg-red-950 border border-red-700 rounded-xl p-6 hidden">
                        <h3 class="font-bold text-lg text-red-400 mb-2 flex items-center space-x-2">
                            <i class="fa-solid fa-triangle-exclamation text-red-400 animate-pulse"></i>
                            <span>CRITICAL: Overdue Tasks Alert</span>
                        </h3>
                        <p class="text-sm text-red-200 mb-4">The following tasks have passed their due date without being Completed or Approved. Please assign immediate resources or resolve blockers.</p>
                        <div class="overflow-x-auto">
                            <table class="w-full text-left border-collapse text-xs text-red-200">
                                <thead>
                                    <tr class="border-b border-red-700 text-red-300 font-bold uppercase tracking-wider">
                                        <th class="py-2 px-3">Task ID</th>
                                        <th class="py-2 px-3">Title</th>
                                        <th class="py-2 px-3">Assignee</th>
                                        <th class="py-2 px-3">Region</th>
                                        <th class="py-2 px-3">Due Date</th>
                                        <th class="py-2 px-3">Status</th>
                                    </tr>
                                </thead>
                                <tbody id="overdueTasksTableBody">
                                    <!-- Populated dynamically -->
                                </tbody>
                            </table>
                        </div>
                    </div>

                    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
                        <div class="lg:col-span-8 bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <h3 class="font-bold text-lg text-emerald-300 mb-2 flex items-center space-x-2">
                                <i class="fa-solid fa-shield-halved"></i>
                                <span>Policy Shield Verification Activity</span>
                            </h3>
                            <p class="text-sm text-slate-300 mb-4">Every action on the dashboard requires both client-side and backend NATS-coupled validation checks. Switching personas changes what is accessible.</p>
                            <button onclick="downloadAuditExcel()" class="inline-block bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold py-2.5 px-4 rounded-lg transition duration-200">
                                <i class="fa-solid fa-download mr-1"></i> Export Live SQLite Log to Excel
                            </button>
                        </div>
                        <div class="lg:col-span-4 bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md flex flex-col h-[280px]">
                            <h4 class="font-bold text-xs uppercase text-slate-400 mb-3 tracking-wider">System Live Audit Log</h4>
                            <div id="miniSqliteLogs" class="flex-1 overflow-y-auto space-y-2 pr-1 font-mono text-[10px] text-slate-300">
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Module B: Inventory View -->
                <div id="view_inventory" class="hidden space-y-6">
                    <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4">
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">Total Items</span>
                            <div class="text-3xl font-bold text-white mt-2" id="statInvTotal">0</div>
                        </div>
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4">
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">In Stock</span>
                            <div class="text-3xl font-bold text-emerald-400 mt-2" id="statInvStock">0</div>
                        </div>
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4">
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">Assigned</span>
                            <div class="text-3xl font-bold text-sky-400 mt-2" id="statInvAssigned">0</div>
                        </div>
                        <div class="bg-brand-500 rounded-xl border border-rose-600 p-4">
                            <span class="text-xs text-rose-400 uppercase tracking-widest font-bold flex items-center gap-1"><i class="fa-solid fa-triangle-exclamation"></i> Low Stock Alerts</span>
                            <div class="text-3xl font-bold text-rose-400 mt-2" id="statInvLowStock">0</div>
                        </div>
                    </div>

                    <div id="inventorySection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2">
                                <i class="fa-solid fa-boxes-stacked text-emerald-400"></i>
                                <span>Serialized Inventory Asset Tracking</span>
                            </h3>
                            <span id="inventoryHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">STRICT SERIAL CONTROL</span>
                        </div>

                        <form id="createItemForm" onsubmit="createInventoryItem(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Item Name</label>
                                <input type="text" id="itemName" placeholder="Huawei GPON ONU" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Serial Number</label>
                                <input type="text" id="itemSerial" placeholder="SN-HUA-7700" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1" title="Auto-queues a procurement order when in-stock count for this item name drops to or below this number">Reorder Threshold</label>
                                <input type="number" id="itemReorderThreshold" min="0" value="0" placeholder="5" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div class="flex items-end">
                                <button id="createItemBtn" type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
                                    Create Serial Asset
                                </button>
                            </div>
                        </form>

                        <div class="flex items-center gap-4 mb-4 text-xs font-bold">
                            <button onclick="setInventoryFilter('all')" id="invFilter_all" class="text-emerald-400 border-b-2 border-emerald-400 pb-1">All Items</button>
                            <button onclick="setInventoryFilter('In_Stock')" id="invFilter_In_Stock" class="text-slate-400 hover:text-slate-200 pb-1">In Stock</button>
                            <button onclick="setInventoryFilter('Assigned')" id="invFilter_Assigned" class="text-slate-400 hover:text-slate-200 pb-1">Assigned</button>
                            <button onclick="setInventoryFilter('low_stock')" id="invFilter_low_stock" class="text-slate-400 hover:text-slate-200 pb-1">Low Stock</button>
                        </div>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[700px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Item ID</th>
                                        <th class="py-3 px-4">Name</th>
                                        <th class="py-3 px-4">Serial #</th>
                                        <th class="py-3 px-4">Status</th>
                                        <th class="py-3 px-4">Region</th>
                                        <th class="py-3 px-4">Allocation</th>
                                        <th class="py-3 px-4">Reorder At</th>
                                        <th class="py-3 px-4">Action</th>
                                    </tr>
                                </thead>
                                <tbody id="inventoryTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>

                    <!-- Direct Manual Serialized Asset Insertion -->
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-2">
                            <h3 class="font-bold text-md text-emerald-400 flex items-center space-x-2">
                                <i class="fa-solid fa-square-plus"></i>
                                <span>Direct Manual Serialized Asset Registration (Bypass Stream)</span>
                            </h3>
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">Immediate Stock Entry</span>
                        </div>

                        <form id="manualItemForm" onsubmit="addManualInventoryItem(event)" class="grid grid-cols-1 md:grid-cols-5 gap-4 bg-brand-900 p-4 rounded-lg border border-brand-600 text-xs">
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Item Name</label>
                                <input type="text" id="manualItemName" placeholder="Power Adapter 12V" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Serial Number</label>
                                <input type="text" id="manualItemSerial" placeholder="SN-PWR-9988" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Reorder Threshold</label>
                                <input type="number" id="manualItemThreshold" min="0" value="2" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Region</label>
                                <select id="manualItemRegion" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-slate-100 focus:outline-none">
                                    <option value="Nairobi">Nairobi</option>
                                    <option value="Mombasa">Mombasa</option>
                                    <option value="Kisumu">Kisumu</option>
                                </select>
                            </div>
                            <div class="flex items-end">
                                <button type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white font-bold py-2 px-4 rounded transition duration-200">
                                    Add Serialized Asset
                                </button>
                            </div>
                        </form>
                    </div>

                    <!-- Procurement Receipt Confirmation Center -->
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-2">
                            <h3 class="font-bold text-md text-amber-400 flex items-center space-x-2">
                                <i class="fa-solid fa-receipt"></i>
                                <span>Procurement Receipt Center (Low-Stock Self-Healing)</span>
                            </h3>
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">Image Barcode Verification</span>
                        </div>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[700px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-2 px-3">Order ID</th>
                                        <th class="py-2 px-3">Item Name</th>
                                        <th class="py-2 px-3">Status</th>
                                        <th class="py-2 px-3">Barcode Attachment</th>
                                        <th class="py-2 px-3">Verified SN</th>
                                        <th class="py-2 px-3">Confirmed By</th>
                                        <th class="py-2 px-3 text-right">Action</th>
                                    </tr>
                                </thead>
                                <tbody id="procurementReceiptTableBody" class="divide-y divide-brand-600 text-xs font-mono">
                                </tbody>
                            </table>
                        </div>
                    </div>

                    <!-- Customer Support & Troubleshooting Tickets -->
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm space-y-6">
                        <div class="flex items-center justify-between border-b border-brand-600 pb-3">
                            <h3 class="font-bold text-md text-emerald-400 flex items-center gap-2">
                                <i class="fa-solid fa-headset"></i>
                                <span>Customer Support Tickets &amp; Troubleshooting Desk</span>
                            </h3>
                            <span class="text-xs text-slate-400 uppercase font-bold tracking-widest font-mono">Convert Tickets to Dispatch Tasks</span>
                        </div>

                        <!-- Raise Support Ticket form -->
                        <form id="createTicketForm" onsubmit="createSupportTicket(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 p-4 bg-brand-900 rounded-lg border border-brand-600 text-xs">
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Select Client ID</label>
                                <select id="ticketCustID" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-slate-100 focus:outline-none">
                                </select>
                            </div>
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Issue / Ticket Title</label>
                                <input type="text" id="ticketTitle" placeholder="Fibre drops packets constantly" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-slate-400 font-bold mb-1">Problem Description</label>
                                <input type="text" id="ticketDesc" placeholder="Drops line every 10 mins during STK push" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-slate-100 focus:outline-none">
                            </div>
                            <div class="flex items-end">
                                <button type="submit" class="w-full bg-indigo-600 hover:bg-indigo-500 text-white font-bold py-2 px-4 rounded transition duration-200">
                                    File Support Ticket
                                </button>
                            </div>
                        </form>

                        <!-- Support Tickets Ledger -->
                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-xs min-w-[700px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 font-semibold uppercase">
                                        <th class="py-2.5 px-3">Ticket ID</th>
                                        <th class="py-2.5 px-3">Client</th>
                                        <th class="py-2.5 px-3">Issue Title</th>
                                        <th class="py-2.5 px-3">Description</th>
                                        <th class="py-2.5 px-3">Status</th>
                                        <th class="py-2.5 px-3">Linked Task ID</th>
                                        <th class="py-2.5 px-3 text-right">Escalation Action</th>
                                    </tr>
                                </thead>
                                <tbody id="ticketsTableBody" class="divide-y divide-brand-600 text-slate-300 font-mono text-[11px]">
                                    <tr><td colspan="7" class="p-3 text-slate-500 italic text-center">No active support tickets.</td></tr>
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module C: Finance View -->
                <div id="view_finance" class="hidden space-y-6">
                    <div id="financeSection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2">
                                <i class="fa-solid fa-file-invoice-dollar text-amber-400"></i>
                                <span>Finance &amp; M-Pesa Micro-Payments</span>
                            </h3>
                            <span id="financeHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">TRUST-BASED RECONCILIATION</span>
                        </div>

                        <form id="recordPaymentForm" onsubmit="recordPayment(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Invoice ID</label>
                                <select id="payInvoiceId" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                                </select>
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Amount (KSh)</label>
                                <input type="number" id="payAmount" placeholder="3000" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">M-Pesa Reference</label>
                                <input type="text" id="payRef" placeholder="QRE99816AH" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div class="flex items-end">
                                <button id="paymentBtn" type="submit" class="w-full bg-amber-600 hover:bg-amber-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
                                    Push M-Pesa STK
                                </button>
                            </div>
                        </form>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[600px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Invoice ID</th>
                                        <th class="py-3 px-4">Total Amount</th>
                                        <th class="py-3 px-4">Paid Amount</th>
                                        <th class="py-3 px-4">Balance Due</th>
                                        <th class="py-3 px-4">Status</th>
                                    </tr>
                                </thead>
                                <tbody id="financeTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module D: Tasks/Timesheets View -->
                <div id="view_tasks" class="hidden space-y-6">
                    <div id="tasksSection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-3">
                            <h3 class="font-bold text-lg flex items-center space-x-2 text-sky-400">
                                <i class="fa-solid fa-list-check"></i>
                                <span>FTTH Tasks &amp; Timesheets Requisitions</span>
                            </h3>
                            <span id="tasksHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">HIERARCHICAL REQUISITIONS</span>
                        </div>

                        <!-- Active material requests panel -->
                        <div class="bg-brand-900 border border-brand-600 rounded-xl p-4 space-y-3 mb-6">
                            <h4 class="font-bold text-xs uppercase text-slate-400">Hardware / Routers Material Requests</h4>
                            <div class="overflow-x-auto w-full max-w-full block">
                                <table class="w-full text-left text-xs min-w-[600px]">
                                    <thead>
                                        <tr class="border-b border-brand-600 text-slate-400 font-semibold uppercase">
                                            <th class="py-2 px-3">Request ID</th>
                                            <th class="py-2 px-3">Task ID</th>
                                            <th class="py-2 px-3">Technician</th>
                                            <th class="py-2 px-3">Requested Item</th>
                                            <th class="py-2 px-3">Status</th>
                                            <th class="py-2 px-3">Issued Serial</th>
                                            <th class="py-2 px-3">Action</th>
                                        </tr>
                                    </thead>
                                    <tbody id="materialsTableBody" class="divide-y divide-brand-600 text-slate-300 font-mono">
                                    </tbody>
                                </table>
                            </div>
                        </div>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[600px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Timesheet ID</th>
                                        <th class="py-3 px-4">Task ID</th>
                                        <th class="py-3 px-4">Technician</th>
                                        <th class="py-3 px-4">Logged Hours</th>
                                        <th class="py-3 px-4">Status</th>
                                        <th class="py-3 px-4">Approved By</th>
                                        <th class="py-3 px-4">Action</th>
                                    </tr>
                                </thead>
                                <tbody id="timesheetsTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>

                    <!-- Requisitions Invoice Notes & Reconciliation Desk -->
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm mt-6">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-2">
                            <h3 class="font-bold text-md text-emerald-400 flex items-center space-x-2">
                                <i class="fa-solid fa-file-invoice-dollar"></i>
                                <span>Requisitions Invoice Notes &amp; Billing Reconciliation</span>
                            </h3>
                            <span class="text-xs text-slate-400 uppercase tracking-widest font-bold">Billing Compliance Guard</span>
                        </div>

                        <p class="text-xs text-slate-300 mb-4">
                            Once serialized equipment is allocated, specify its usage type below. Standard internal usage support is automatically cleared. Customer installations or broken equipment generates customer invoices and undergoes strict reconciliation controls on task completion.
                        </p>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-xs min-w-[700px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 font-semibold uppercase">
                                        <th class="py-2.5 px-3">Reconciliation ID</th>
                                        <th class="py-2.5 px-3">Item Name</th>
                                        <th class="py-2.5 px-3">Serial #</th>
                                        <th class="py-2.5 px-3">Task ID</th>
                                        <th class="py-2.5 px-3">Requester</th>
                                        <th class="py-2.5 px-3">Usage Type Select</th>
                                        <th class="py-2.5 px-3">Reconciliation Status</th>
                                        <th class="py-2.5 px-3">Attached Invoice</th>
                                        <th class="py-2.5 px-3">Linked Payment</th>
                                    </tr>
                                </thead>
                                <tbody id="invoiceNotesTableBody" class="divide-y divide-brand-600 font-mono text-slate-300 text-[11px]">
                                    <tr><td colspan="9" class="p-3 text-slate-500 italic text-center">No invoice notes issued.</td></tr>
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module E: Customer CRM View -->
                <div id="view_customers" class="hidden space-y-6">
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2 text-emerald-400">
                                <i class="fa-solid fa-users"></i>
                                <span>Customer Relationship Management (CRM)</span>
                            </h3>
                            <span class="text-xs text-slate-400 uppercase">CLIENT PROFILES</span>
                        </div>

                        <!-- Create Customer Form -->
                        <form id="createCustomerForm" onsubmit="createCustomer(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Customer ID</label>
                                <input type="text" id="custID" placeholder="cust_karanja" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Full Name</label>
                                <input type="text" id="custName" placeholder="David Karanja" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Phone Number</label>
                                <input type="text" id="custPhone" placeholder="254711223344" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div class="flex items-end">
                                <button type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
                                    Create Client Record
                                </button>
                            </div>
                        </form>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[700px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Customer ID</th>
                                        <th class="py-3 px-4">Name</th>
                                        <th class="py-3 px-4">Phone</th>
                                        <th class="py-3 px-4">Attached Device</th>
                                        <th class="py-3 px-4">Invoice Status</th>
                                        <th class="py-3 px-4">Dispatch Status</th>
                                        <th class="py-3 px-4 text-right">Actions</th>
                                    </tr>
                                </thead>
                                <tbody id="customersTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module F: User Directory & Visual Team Hierarchy -->
                <div id="view_users" class="hidden space-y-6">
                    <!-- Tab Headers -->
                    <div class="flex border-b border-brand-600 gap-4 mb-4">
                        <button id="usersTab_directory" onclick="switchUsersTab('directory')" class="text-sm font-bold text-emerald-400 border-b-2 border-emerald-400 pb-2 flex items-center gap-2">
                            <i class="fa-solid fa-users"></i>
                            <span>Personnel Directory & Invitations</span>
                        </button>
                        <button id="usersTab_tree" onclick="switchUsersTab('tree')" class="text-sm font-bold text-slate-400 hover:text-slate-200 pb-2 flex items-center gap-2">
                            <i class="fa-solid fa-sitemap"></i>
                            <span>Visual Team Tree Hierarchy</span>
                        </button>
                    </div>

                    <!-- Directory Panel -->
                    <div id="usersPanel_directory" class="space-y-6">
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                            <div class="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-4">
                                <div>
                                    <h3 class="font-bold text-lg flex items-center space-x-2 text-indigo-400">
                                        <i class="fa-solid fa-user-gear"></i>
                                        <span>User Directory &amp; Role Management</span>
                                    </h3>
                                    <p class="text-xs text-slate-300 mt-1">Manage active workspace teammate accounts, roles, and credential lifecycles</p>
                                </div>
                                <button onclick="openInviteModal()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold py-2.5 px-4 rounded-lg transition duration-200 flex items-center gap-2">
                                    <i class="fa-solid fa-user-plus"></i>
                                    <span>Invite Workspace Teammate</span>
                                </button>
                            </div>

                            <div class="overflow-x-auto w-full max-w-full block">
                                <table class="w-full text-left text-sm min-w-[600px]">
                                    <thead>
                                        <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                            <th class="py-3 px-4">User ID</th>
                                            <th class="py-3 px-4">Full Name</th>
                                            <th class="py-3 px-4">Assigned Role</th>
                                            <th class="py-3 px-4">Working Region</th>
                                            <th class="py-3 px-4 text-right">Credential Management</th>
                                        </tr>
                                    </thead>
                                    <tbody id="usersTableBody" class="divide-y divide-brand-600">
                                    </tbody>
                                </table>
                            </div>
                        </div>

                        <!-- Active/Pending invitations -->
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                            <h3 class="font-bold text-sm uppercase text-slate-400 mb-3 flex items-center gap-2">
                                <i class="fa-solid fa-paper-plane text-emerald-400"></i>
                                <span>Pending Workspace Invitations</span>
                            </h3>
                            <div class="overflow-x-auto w-full max-w-full block">
                                <table class="w-full text-left text-xs min-w-[600px]">
                                    <thead>
                                        <tr class="border-b border-brand-600 text-slate-400 font-semibold uppercase">
                                            <th class="py-2 px-3">Email Address</th>
                                            <th class="py-2 px-3">Full Name</th>
                                            <th class="py-2 px-3">Assigned Role</th>
                                            <th class="py-2 px-3">Working Region</th>
                                            <th class="py-2 px-3">Status</th>
                                            <th class="py-2 px-3 text-right">Activation Link</th>
                                        </tr>
                                    </thead>
                                    <tbody id="invitationsTableBody" class="divide-y divide-brand-600 font-mono text-[11px] text-slate-300">
                                        <tr><td colspan="6" class="p-3 text-slate-500 italic text-center">No pending invitations found.</td></tr>
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </div>

                    <!-- Visual Hierarchy tree panel -->
                    <div id="usersPanel_tree" class="hidden space-y-4">
                        <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                            <h3 class="font-bold text-lg flex items-center space-x-2 text-emerald-400 mb-2">
                                <i class="fa-solid fa-network-wired"></i>
                                <span>Teammate Reporting Lines &amp; Organogram</span>
                            </h3>
                            <p class="text-xs text-slate-300 mb-6">Build the workspace reporting tree hierarchy. Superior roles can edit and supervise subordinate tasks/timesheets. Update reporting lines below by changing the dropdowns.</p>
                            <div id="orgTreeContainer" class="space-y-4 overflow-x-auto p-4 bg-brand-900/40 rounded-xl border border-brand-600/50">
                                <!-- Tree rendered recursively -->
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Module G: Casbin Policy View -->
                <div id="view_rbac" class="hidden space-y-6">
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-3">
                            <h3 class="font-bold text-lg flex items-center space-x-2 text-emerald-400">
                                <i class="fa-solid fa-shield-halved"></i>
                                <span>Interactive Policy Engine Manager (ABAC/RBAC)</span>
                            </h3>
                            <span class="text-xs px-2 py-0.5 bg-brand-900 text-emerald-400 font-mono rounded font-bold border border-brand-600">TenantAdmin Control Only</span>
                        </div>
                        <p class="text-xs text-slate-300 mb-4">Toggle dynamic role permissions in the database instantly. When updated, Go's hierarchy resolver re-calculates all backend and frontend capabilities in real time.</p>

                        <div id="policyConfigGrid" class="grid grid-cols-1 md:grid-cols-2 gap-4">
                        </div>
                    </div>
                </div>

            </div>
        </div>
    </div>

    <!-- REGISTER TENANT MODAL -->
    <div id="registerModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Create Your Account</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Full Name</label>
                <input type="text" id="regName" placeholder="David Karanja" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Email</label>
                <input type="email" id="regEmail" placeholder="david@example.com" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Password (min. 8 characters)</label>
                <input type="password" id="regPassword" placeholder="••••••••" required minlength="8" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none" oninput="validatePasswordStrength('regPassword', 'regPass')">
                <div id="regPassChecklist" class="mt-1.5 p-1.5 bg-brand-950/40 rounded border border-brand-600/30 space-y-1 text-[11px]">
                    <div id="regPassLength"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">At least 8 characters</span></div>
                    <div id="regPassUpper"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One uppercase letter</span></div>
                    <div id="regPassLower"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One lowercase letter</span></div>
                    <div id="regPassDigit"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One digit (0-9)</span></div>
                    <div id="regPassSpecial"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One special character (!@#$%^&* etc.)</span></div>
                </div>
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Working Region</label>
                <select id="regRegion" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                    <option value="Nairobi">Nairobi</option>
                    <option value="Mombasa">Mombasa</option>
                    <option value="Kisumu">Kisumu</option>
                </select>
            </div>

            <div class="pt-2 border-t border-brand-600 space-y-2">
                <div class="flex text-xs font-bold text-slate-300 space-x-4">
                    <label class="flex items-center space-x-1"><input type="radio" name="regMode" value="join" checked onchange="toggleRegisterMode()"> <span>Join existing org</span></label>
                    <label class="flex items-center space-x-1"><input type="radio" name="regMode" value="create" onchange="toggleRegisterMode()"> <span>Create new org</span></label>
                </div>
                <div id="regJoinField">
                    <label class="block text-xs text-slate-400 font-bold mb-1">Tenant ID to join</label>
                    <input type="text" id="regTenantId" placeholder="tenant_safari" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                    <p class="text-[10px] text-slate-500 mt-1">You'll join as a field technician; an admin can promote you afterward.</p>
                </div>
                <div id="regCreateField" class="hidden">
                    <label class="block text-xs text-slate-400 font-bold mb-1">New organization name</label>
                    <input type="text" id="regTenantName" placeholder="Pioneer ISP Tech Services" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                    <p class="text-[10px] text-slate-500 mt-1">You'll become this organization's admin.</p>
                </div>
            </div>

            <div id="registerError" class="hidden text-xs text-rose-400 font-semibold"></div>

            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeRegisterModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitRegistration()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Create Account
                </button>
            </div>
        </div>
    </div>

    <!-- CONVERT TICKET TO TASK MODAL -->
    <div id="convertTicketModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <div class="flex justify-between items-center border-b border-brand-600 pb-2">
                <h4 class="font-bold text-md text-emerald-300 flex items-center gap-2">
                    <i class="fa-solid fa-wrench"></i>
                    <span>Convert Ticket to Task</span>
                </h4>
                <button onclick="closeConvertModal()" class="text-slate-400 hover:text-slate-200"><i class="fa-solid fa-xmark"></i></button>
            </div>

            <form id="convertForm" onsubmit="handleConvertTicket(event)" class="space-y-4 text-xs">
                <div>
                    <label class="block text-slate-400 font-bold mb-1">Ticket ID</label>
                    <input type="text" id="convertTicketId" readonly class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-400">
                </div>
                <div>
                    <label class="block text-slate-400 font-bold mb-1">Task Title</label>
                    <input type="text" id="convertTaskTitle" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                </div>
                <div>
                    <label class="block text-slate-400 font-bold mb-1">Assign To (Technician)</label>
                    <select id="convertAssignee" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                    </select>
                </div>
                <div>
                    <label class="block text-slate-400 font-bold mb-1">Task Dependency (Depends On - Optional)</label>
                    <select id="convertDependency" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                        <option value="">No Dependency</option>
                    </select>
                </div>

                <div class="pt-2 border-t border-brand-600 flex justify-end gap-2">
                    <button type="button" onclick="closeConvertModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 font-bold px-4 py-2 rounded">Cancel</button>
                    <button type="submit" class="bg-emerald-600 hover:bg-emerald-500 text-white font-bold px-4 py-2 rounded">Assign Task</button>
                </div>
            </form>
        </div>
    </div>

    <!-- INVITE MEMBER MODAL -->
    <div id="inviteModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-lg w-full space-y-4">
            <div class="flex justify-between items-center border-b border-brand-600 pb-2">
                <h4 class="font-bold text-md text-emerald-300 flex items-center gap-2">
                    <i class="fa-solid fa-envelope-open-text"></i>
                    <span>Invite Teammate to Workspace</span>
                </h4>
                <button onclick="closeInviteModal()" class="text-slate-400 hover:text-slate-200"><i class="fa-solid fa-xmark"></i></button>
            </div>

            <form id="inviteForm" onsubmit="handleSendInvitation(event)" class="space-y-4 text-xs">
                <div class="grid grid-cols-2 gap-4">
                    <div>
                        <label class="block text-[11px] font-bold text-slate-400 mb-1">Full Name</label>
                        <input type="text" id="inviteName" placeholder="Jordan Smith" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                    </div>
                    <div>
                        <label class="block text-[11px] font-bold text-slate-400 mb-1">Professional Email</label>
                        <input type="email" id="inviteEmail" placeholder="j.smith@nexus-crm.io" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                    </div>
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div>
                        <label class="block text-[11px] font-bold text-slate-400 mb-1">Working Region</label>
                        <select id="inviteRegion" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                            <option value="Nairobi">Nairobi</option>
                            <option value="Mombasa">Mombasa</option>
                            <option value="Kisumu">Kisumu</option>
                        </select>
                    </div>
                    <div>
                        <label class="block text-[11px] font-bold text-slate-400 mb-1">Assign Reporting Manager</label>
                        <select id="inviteManager" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-slate-200 focus:outline-none">
                            <option value="">No Reporting Manager</option>
                        </select>
                    </div>
                </div>

                <div>
                    <label class="block text-[11px] font-bold text-slate-400 mb-1">Role Assignment</label>
                    <div class="grid grid-cols-2 gap-2">
                        <label class="flex items-center gap-2 p-2 border border-brand-600 rounded bg-brand-900/40 cursor-pointer hover:bg-brand-600/30">
                            <input type="radio" name="inviteRole" value="field_technician" checked>
                            <div>
                                <p class="font-bold text-white">Technician</p>
                                <p class="text-[9px] text-slate-400">Field work & timesheets</p>
                            </div>
                        </label>
                        <label class="flex items-center gap-2 p-2 border border-brand-600 rounded bg-brand-900/40 cursor-pointer hover:bg-brand-600/30">
                            <input type="radio" name="inviteRole" value="manager">
                            <div>
                                <p class="font-bold text-white">Manager</p>
                                <p class="text-[9px] text-slate-400">Approvals & team building</p>
                            </div>
                        </label>
                        <label class="flex items-center gap-2 p-2 border border-brand-600 rounded bg-brand-900/40 cursor-pointer hover:bg-brand-600/30">
                            <input type="radio" name="inviteRole" value="finance_officer">
                            <div>
                                <p class="font-bold text-white">Finance</p>
                                <p class="text-[9px] text-slate-400">Ledger & micro-payments</p>
                            </div>
                        </label>
                        <label class="flex items-center gap-2 p-2 border border-brand-600 rounded bg-brand-900/40 cursor-pointer hover:bg-brand-600/30">
                            <input type="radio" name="inviteRole" value="tenant_admin">
                            <div>
                                <p class="font-bold text-white">Admin</p>
                                <p class="text-[9px] text-slate-400">Full system override</p>
                            </div>
                        </label>
                    </div>
                </div>

                <div class="pt-2 border-t border-brand-600 flex justify-end gap-2">
                    <button type="button" onclick="closeInviteModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 font-bold px-4 py-2 rounded">Cancel</button>
                    <button type="submit" class="bg-emerald-600 hover:bg-emerald-500 text-white font-bold px-4 py-2 rounded flex items-center gap-2">
                        <span>Send Invitation</span>
                        <i class="fa-solid fa-paper-plane"></i>
                    </button>
                </div>
            </form>
        </div>
    </div>

    <!-- ALLOCATION MODAL -->
    <div id="allocationModal" class="hidden fixed inset-0 bg-black bg-opacity-70 flex items-center justify-center p-4 z-50">
        <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Allocate Device to Technician</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Item ID</label>
                <input type="text" id="modalItemId" readonly class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-400 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Technician</label>
                <select id="modalTechId" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                </select>
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitAllocation()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Allocate Asset
                </button>
            </div>
        </div>
    </div>

    <!-- PASSWORD RESET MODAL -->
    <div id="passwordResetModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Reset User Password & Credentials</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">User ID</label>
                <input type="text" id="resetModalUserId" readonly class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-400">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Enter New Password</label>
                <input type="password" id="resetModalNewPassword" placeholder="e.g. ksh8890" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none" oninput="validatePasswordStrength('resetModalNewPassword', 'resetPass')">
                <div id="resetPassChecklist" class="mt-1.5 p-1.5 bg-brand-950/40 rounded border border-brand-600/30 space-y-1 text-[11px]">
                    <div id="resetPassLength"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">At least 8 characters</span></div>
                    <div id="resetPassUpper"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One uppercase letter</span></div>
                    <div id="resetPassLower"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One lowercase letter</span></div>
                    <div id="resetPassDigit"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One digit (0-9)</span></div>
                    <div id="resetPassSpecial"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One special character (!@#$%^&* etc.)</span></div>
                </div>
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closePasswordResetModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitPasswordReset()" class="bg-amber-600 hover:bg-amber-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Commit Reset
                </button>
            </div>
        </div>
    </div>

    <!-- SUBMIT MATERIAL REQUEST MODAL -->
    <div id="materialRequestModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Submit Hardware Requisition</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Paused Task</label>
                <select id="matModalTaskId" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                </select>
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Router Equipment</label>
                <select id="matModalItemName" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                    <option value="Huawei GPON ONU">Huawei GPON ONU</option>
                    <option value="LaserJet Fuser Assembly">LaserJet Fuser Assembly</option>
                </select>
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeMaterialRequestModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitMaterialRequest()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Pause Job & Request
                </button>
            </div>
        </div>
    </div>

    <script>
        function validatePasswordStrength(inputId, prefix) {
            const val = document.getElementById(inputId).value;
            const criteria = {
                Length: { ok: val.length >= 8, txt: "At least 8 characters" },
                Upper: { ok: /[A-Z]/.test(val), txt: "One uppercase letter" },
                Lower: { ok: /[a-z]/.test(val), txt: "One lowercase letter" },
                Digit: { ok: /[0-9]/.test(val), txt: "One digit (0-9)" },
                Special: { ok: /[^A-Za-z0-9]/.test(val), txt: "One special character (!@#$%^&* etc.)" }
            };

            for (const key in criteria) {
                const el = document.getElementById(prefix + key);
                if (!el) continue;
                const item = criteria[key];
                if (item.ok) {
                    el.innerHTML = '<i class="fa-solid fa-circle-check text-emerald-400"></i> <span class="text-emerald-400">' + item.txt + '</span>';
                } else {
                    el.innerHTML = '<i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">' + item.txt + '</span>';
                }
            }
        }

        let currentState = {};
        let currentHeaders = {};
        let currentUser = {};
        let authToken = '';
        let activeView = 'dashboard';
        let inventoryFilter = 'all';
        let loggedIn = false;
        let recentNotifications = [];
        let mobileSidebarOpen = false;

        // On page load, try to resume a session from a previously-stored
        // token rather than forcing a fresh login on every refresh. If the
        // token is missing/expired, /api/me 401s and we fall through to the
        // login gate — no state or user list is ever fetched pre-auth.
        async function initAuth() {
            const saved = sessionStorage.getItem('erp_token');
            if (!saved) return;
            authToken = saved;
            currentHeaders = { 'Authorization': 'Bearer ' + authToken, 'Content-Type': 'application/json' };
            try {
                const res = await fetch('/api/me', { headers: currentHeaders });
                if (!res.ok) throw new Error('session expired');
                currentUser = await res.json();
                await enterWorkspace();
            } catch (err) {
                sessionStorage.removeItem('erp_token');
                authToken = '';
                currentHeaders = {};
            }
        }

        function showLoginError(elId, message) {
            const el = document.getElementById(elId);
            el.textContent = message;
            el.classList.remove('hidden');
        }

        async function handleLogin(e) {
            e.preventDefault();
            const email = document.getElementById('loginEmail').value;
            const tenant_id = document.getElementById('loginTenantID').value.trim();
            const password = document.getElementById('loginPassword').value;
            document.getElementById('loginError').classList.add('hidden');

            try {
                const res = await fetch('/api/login', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ email, password, tenant_id })
                });
                const r = await res.json();
                if (!res.ok) {
                    showLoginError('loginError', r.error || 'Login failed');
                    return;
                }
                authToken = r.token;
                currentUser = r;
                sessionStorage.setItem('erp_token', authToken);
                currentHeaders = { 'Authorization': 'Bearer ' + authToken, 'Content-Type': 'application/json' };
                document.getElementById('loginPassword').value = '';
                await enterWorkspace();
            } catch (err) {
                showLoginError('loginError', 'Network error — is the server reachable?');
            }
        }

        function openRegisterModal() {
            document.getElementById('registerModal').classList.remove('hidden');
        }
        function closeRegisterModal() {
            document.getElementById('registerModal').classList.add('hidden');
        }
        function toggleRegisterMode() {
            const mode = document.querySelector('input[name="regMode"]:checked').value;
            document.getElementById('regJoinField').classList.toggle('hidden', mode !== 'join');
            document.getElementById('regCreateField').classList.toggle('hidden', mode !== 'create');
        }

        async function submitRegistration() {
            const mode = document.querySelector('input[name="regMode"]:checked').value;
            const payload = {
                name: document.getElementById('regName').value,
                email: document.getElementById('regEmail').value,
                password: document.getElementById('regPassword').value,
                region: document.getElementById('regRegion').value,
            };
            if (mode === 'join') {
                payload.tenant_id = document.getElementById('regTenantId').value;
            } else {
                payload.tenant_name = document.getElementById('regTenantName').value;
            }

            document.getElementById('registerError').classList.add('hidden');
            try {
                const res = await fetch('/api/register', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                const r = await res.json();
                if (!res.ok) {
                    showLoginError('registerError', r.error || 'Registration failed');
                    return;
                }
                authToken = r.token;
                currentUser = r;
                sessionStorage.setItem('erp_token', authToken);
                currentHeaders = { 'Authorization': 'Bearer ' + authToken, 'Content-Type': 'application/json' };
                closeRegisterModal();
                await enterWorkspace();
            } catch (err) {
                showLoginError('registerError', 'Network error — is the server reachable?');
            }
        }

        // enterWorkspace transitions from the login gate to the dashboard
        // shell once currentUser/authToken are set (by login, registration,
        // or session restore), then loads live state.
        async function enterWorkspace() {
            loggedIn = true;
            document.getElementById('loginGate').classList.add('hidden');
            document.getElementById('dashboardApp').classList.remove('hidden');

            document.getElementById('sidebarUserName').innerText = currentUser.name;
            document.getElementById('sidebarRoleLabel').innerText = currentUser.role_name;
            document.getElementById('sidebarAvatar').innerText = currentUser.name.charAt(0);

            await fetchState();
            await fetchLogs();

            const tenantObj = currentState.tenants ? currentState.tenants[currentUser.tenant_id] : null;
            const tenantNameStr = tenantObj ? tenantObj.name : currentUser.tenant_id;
            document.getElementById('sidebarTenantName').innerText = tenantNameStr;
            document.getElementById('mobileTenantTitle').innerText = tenantNameStr;

            pushNotification('SESSION_LOGGED_IN', 'User ' + currentUser.name + ' entered the ' + tenantNameStr + ' workspace.');
            switchModuleView('dashboard');
        }

        function handleLogout() {
            loggedIn = false;
            authToken = '';
            currentUser = {};
            currentHeaders = {};
            currentState = {};
            sessionStorage.removeItem('erp_token');
            document.getElementById('loginPassword').value = '';
            document.getElementById('dashboardApp').classList.add('hidden');
            document.getElementById('loginGate').classList.remove('hidden');
            closeMobileSidebar();
        }

        function toggleMobileSidebar() {
            const sidebar = document.getElementById('sidebarDrawer');
            const icon = document.getElementById('hamburgerIcon');

            if (mobileSidebarOpen) {
                sidebar.classList.add('hidden');
                sidebar.classList.remove('flex', 'absolute', 'w-64', 'h-screen');
                icon.className = 'fa-solid fa-bars text-lg';
                mobileSidebarOpen = false;
            } else {
                sidebar.classList.remove('hidden');
                sidebar.classList.add('flex', 'absolute', 'w-64', 'h-screen');
                icon.className = 'fa-solid fa-xmark text-lg';
                mobileSidebarOpen = true;
            }
        }

        function closeMobileSidebar() {
            if (mobileSidebarOpen) {
                toggleMobileSidebar();
            }
        }

        function setInventoryFilter(filter) {
            inventoryFilter = filter;
            ['all', 'In_Stock', 'Assigned', 'low_stock'].forEach(f => {
                const btn = document.getElementById('invFilter_' + f);
                if (!btn) return;
                btn.className = f === filter
                    ? 'text-emerald-400 border-b-2 border-emerald-400 pb-1'
                    : 'text-slate-400 hover:text-slate-200 pb-1';
            });
            renderDashboard();
        }

        function switchModuleView(viewName) {
            activeView = viewName;

            const views = ['dashboard', 'inventory', 'finance', 'tasks', 'customers', 'users', 'rbac'];
            views.forEach(v => {
                const el = document.getElementById('view_' + v);
                if (el) el.classList.add('hidden');

                const link = document.getElementById('navLink_' + v);
                if (link) link.className = 'w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition';
            });

            const activeEl = document.getElementById('view_' + viewName);
            if (activeEl) activeEl.classList.remove('hidden');

            const link = document.getElementById('navLink_' + viewName);
            if (link) link.className = 'w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg bg-emerald-600 text-white font-bold transition';

            let title = 'Dashboard Home';
            if (viewName === 'inventory') title = 'Inventory Serial Tracking';
            else if (viewName === 'finance') title = 'Finance &amp; M-Pesa payments';
            else if (viewName === 'tasks') title = 'Field Materials &amp; Timesheets';
            else if (viewName === 'customers') title = 'Customer relationship (CRM)';
            else if (viewName === 'users') title = 'Personnel &amp; Key Directory';
            else if (viewName === 'rbac') title = 'RBAC Policy Engine';

            document.getElementById('currentModuleTitle').innerHTML = title;
            closeMobileSidebar();
        }

        function pushNotification(type, message) {
            recentNotifications.unshift({
                timestamp: new Date().toLocaleTimeString(),
                type: type,
                message: message
            });
            if (recentNotifications.length > 20) recentNotifications.pop();

            document.getElementById('notifBadge').classList.remove('hidden');
            document.getElementById('mobileNotifBadge').classList.remove('hidden');
            renderNotifications();
        }

        function toggleNotificationPane() {
            const pane = document.getElementById('notifPane');
            pane.classList.toggle('hidden');
            document.getElementById('notifBadge').classList.add('hidden');
            document.getElementById('mobileNotifBadge').classList.add('hidden');
        }

        function clearNotifications() {
            recentNotifications = [];
            renderNotifications();
        }

        function renderNotifications() {
            const container = document.getElementById('notifList');
            container.innerHTML = '';

            if (recentNotifications.length === 0) {
                container.innerHTML = '<div class="p-2 bg-brand-900 rounded border border-brand-600 italic text-slate-500">No recent notifications logged.</div>';
                return;
            }

            recentNotifications.forEach(n => {
                const div = document.createElement('div');
                div.className = 'p-2 bg-brand-900 rounded border border-brand-600 flex items-center justify-between';
                div.innerHTML = '<span>[' + n.timestamp + '] <strong class="text-white">' + n.type + ':</strong> ' + n.message + '</span>';
                container.appendChild(div);
            });
        }

        async function fetchState() {
            if (!loggedIn) return;
            try {
                const res = await fetch('/api/state', { headers: currentHeaders });
                const data = await res.json();
                currentState = data;
                renderDashboard();
            } catch (err) {
                console.error('Error loading state:', err);
            }
        }

        async function downloadAuditExcel() {
            try {
                const res = await fetch('/api/exports/excel', { headers: currentHeaders });
                if (!res.ok) {
                    alert('Export failed — you may need to log in again.');
                    return;
                }
                const blob = await res.blob();
                const url = window.URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = 'audit_log_' + currentUser.tenant_id + '.xlsx';
                document.body.appendChild(a);
                a.click();
                a.remove();
                window.URL.revokeObjectURL(url);
            } catch (err) {
                alert('Export failed: network error.');
            }
        }

        async function fetchLogs() {
            if (!loggedIn) return;
            try {
                const res = await fetch('/api/logs', { headers: currentHeaders });
                const logs = await res.json();

                const miniContainer = document.getElementById('miniSqliteLogs');
                miniContainer.innerHTML = '';

                if (!logs || logs.length === 0) {
                    miniContainer.innerHTML = "<div class='text-slate-500 italic'>No logs found in SQLite.</div>";
                    return;
                }

                logs.slice(0, 10).forEach(l => {
                    const div = document.createElement('div');
                    div.className = 'p-1.5 bg-brand-900 rounded border border-brand-600 flex flex-col space-y-0.5';
                    const t = new Date(l.timestamp).toLocaleTimeString();

                    div.innerHTML = '<div class="flex justify-between font-bold text-slate-400"><span>[' + t + '] ' + l.action + '</span></div>' +
                        '<div class="text-slate-400">' + l.details + '</div>';
                    miniContainer.appendChild(div);
                });
            } catch (err) {
                console.error('Error loading SQLite logs:', err);
            }
        }

        async function togglePermission(roleName, perm, checkbox) {
            const currentRole = currentUser.role_name;
            if (currentRole !== 'tenant_admin') {
                alert('Permission Denied: Only a TenantAdmin is authorized to update RBAC policies.');
                checkbox.checked = !checkbox.checked;
                return;
            }

            const grid = document.getElementById('policyConfigGrid');
            const checkboxes = grid.querySelectorAll('input[data-role="' + roleName + '"]:checked');
            const newPerms = [];
            checkboxes.forEach(cb => {
                newPerms.push(cb.value);
            });

            try {
                const res = await fetch('/api/rbac/update', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ role_name: roleName, permissions: newPerms })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('RBAC_MUTATED', 'Role ' + roleName + ' permission set successfully toggled.');
                    fetchState();
                } else {
                    alert('Error: ' + r.error);
                }
            } catch (err) {
                console.error('Failed to toggle permission:', err);
            }
        }

        function renderDashboard() {
            const activeRole = currentUser.role_name;
            const activeTenant = currentUser.tenant_id;

            // Render Overdue Tasks (Task 4)
            const overdueTasksSection = document.getElementById("overdueTasksSection");
            const overdueTasksTableBody = document.getElementById("overdueTasksTableBody");
            if (overdueTasksSection && overdueTasksTableBody) {
                overdueTasksTableBody.innerHTML = "";
                const oTasks = Object.values(currentState.overdue_tasks || {}).filter(t => t.tenant_id === activeTenant);
                if (oTasks.length > 0) {
                    overdueTasksSection.classList.remove("hidden");
                    oTasks.forEach(task => {
                        const tr = document.createElement("tr");
                        tr.className = "border-b border-red-950 hover:bg-red-900/30 transition duration-150";
                        const dStr = task.due_date ? new Date(task.due_date).toLocaleDateString() : "N/A";
                        tr.innerHTML = "<td class='py-2.5 px-3 font-mono font-bold text-red-300'>" + task.id + "</td>" +
                                       "<td class='py-2.5 px-3'>" + task.title + "</td>" +
                                       "<td class='py-2.5 px-3 font-semibold'>" + (task.assigned_to || "Unassigned") + "</td>" +
                                       "<td class='py-2.5 px-3'>" + task.region + "</td>" +
                                       "<td class='py-2.5 px-3 font-mono text-red-400 font-bold'>" + dStr + "</td>" +
                                       "<td class='py-2.5 px-3'><span class='bg-red-800 text-red-100 text-[10px] font-bold px-2 py-0.5 rounded-full uppercase'>" + task.status + "</span></td>";
                        overdueTasksTableBody.appendChild(tr);
                    });
                } else {
                    overdueTasksSection.classList.add("hidden");
                }
            }

            // 1. Compile side navigation dynamically matching roles / clearance permissions
            const sidebarNav = document.getElementById('sidebarNav');
            sidebarNav.innerHTML = '';

            const activeRolePermissions = currentState.roles[activeRole] || [];

            const navItems = [
                { id: 'dashboard', label: 'Dashboard Home', icon: 'fa-chart-line' },
                { id: 'inventory', label: 'Inventory Control', icon: 'fa-boxes-stacked', perm: 'inventory:read' },
                { id: 'finance', label: 'Finance & Payments', icon: 'fa-file-invoice-dollar', perm: 'finance:read' },
                { id: 'tasks', label: 'Tasks & Requisitions', icon: 'fa-list-check', perm: 'tasks:read' },
                { id: 'customers', label: 'Customer CRM', icon: 'fa-users', perm: 'users:*' },
                { id: 'users', label: 'Personnel Profiles', icon: 'fa-user-gear', perm: 'users:*' },
                { id: 'rbac', label: 'RBAC Policy Config', icon: 'fa-shield-halved', perm: 'users:*' }
            ];

            navItems.forEach(n => {
                const isCleared = !n.perm ||
                    activeRolePermissions.includes(n.perm) ||
                    activeRolePermissions.includes('*') ||
                    activeRolePermissions.some(p => p.endsWith(':*') && n.perm && n.perm.startsWith(p.slice(0, -2)));
                if (!isCleared) return;

                const btn = document.createElement('button');
                btn.id = 'navLink_' + n.id;
                btn.onclick = () => switchModuleView(n.id);

                const activeClass = activeView === n.id ? 'bg-emerald-600 text-white font-bold' : 'text-slate-300 hover:bg-brand-600 hover:text-white';
                btn.className = 'w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg transition ' + activeClass;
                btn.innerHTML = '<i class="fa-solid ' + n.icon + ' w-5 text-center"></i><span>' + n.label + '</span>';
                sidebarNav.appendChild(btn);
            });

            // Fill Quick stat card numbers
            let invCount = 0;
            Object.values(currentState.inventory || {}).forEach(item => { if (item.tenant_id === activeTenant) invCount++; });
            document.getElementById('stat_inventory_cnt').innerText = invCount;

            let finSum = 0;
            Object.values(currentState.invoices || {}).forEach(inv => { if (inv.tenant_id === activeTenant) finSum += inv.total_amount; });
            document.getElementById('stat_finance_val').innerText = 'KSh ' + finSum.toLocaleString();

            let pendingApprovals = 0;
            Object.values(currentState.timesheets || {}).forEach(ts => { if (ts.tenant_id === activeTenant && ts.status === 'Submitted') pendingApprovals++; });
            document.getElementById('stat_tasks_cnt').innerText = pendingApprovals;

            // Render Policy Config manager
            const policyGrid = document.getElementById('policyConfigGrid');
            if (policyGrid) {
                policyGrid.innerHTML = '';
                const allPermsList = ['inventory:read', 'inventory:write', 'inventory:*', 'finance:read', 'finance:write', 'tasks:read', 'tasks:create', 'tasks:approve', 'timesheets:approve', 'timesheets:submit', '*'];

                const rolesKeys = Object.keys(currentState.roles || {}).sort();
                rolesKeys.forEach(roleName => {
                    const rolePerms = currentState.roles[roleName] || [];
                    const card = document.createElement('div');
                    card.className = 'bg-brand-900 border border-brand-600 rounded-lg p-4 space-y-2';

                    let titleColor = 'text-sky-300';
                    if (roleName === 'tenant_admin') titleColor = 'text-rose-400 font-bold';
                    else if (roleName === 'manager') titleColor = 'text-amber-400';

                    card.innerHTML = '<div class="text-xs uppercase font-bold tracking-widest ' + titleColor + '">' + roleName + '</div>';

                    const cbContainer = document.createElement('div');
                    cbContainer.className = 'grid grid-cols-2 gap-x-2 gap-y-1';

                    allPermsList.forEach(perm => {
                        const isChecked = rolePerms.includes(perm) || rolePerms.includes('*') ? 'checked' : '';
                        const label = document.createElement('label');
                        label.className = 'flex items-center space-x-2 text-[11px] text-slate-300 cursor-pointer hover:text-slate-100';
                        const disabledStr = activeRole === 'tenant_admin' ? '' : 'disabled';

                        label.innerHTML = '<input type="checkbox" value="' + perm + '" data-role="' + roleName + '" ' + isChecked + ' ' + disabledStr + ' onchange="togglePermission(\'' + roleName + '\', \'' + perm + '\', this)" class="rounded bg-brand-500 border-brand-600 text-emerald-500 focus:ring-emerald-500"> ' +
                            '<span>' + perm + '</span>';
                        cbContainer.appendChild(label);
                    });

                    card.appendChild(cbContainer);
                    policyGrid.appendChild(card);
                });
            }

            // Render Inventory Control: stat cards + filtered table
            const invBody = document.getElementById('inventoryTableBody');
            if (invBody) {
                invBody.innerHTML = '';
                const canViewInventory = activeRolePermissions.includes('inventory:read') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');
                const canWriteInventory = activeRolePermissions.includes('inventory:write') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');

                if (canViewInventory) {
                    const tenantItems = Object.values(currentState.inventory || {}).filter(item => item.tenant_id === activeTenant);

                    // Low stock = for this item's name, in-stock count <= that name's max reorder threshold.
                    // Mirrors db.CheckReorderThresholdAndTriggerProcurement's own definition server-side.
                    const stockByName = {};
                    const thresholdByName = {};
                    tenantItems.forEach(it => {
                        if (it.status === 'In_Stock') stockByName[it.name] = (stockByName[it.name] || 0) + 1;
                        thresholdByName[it.name] = Math.max(thresholdByName[it.name] || 0, it.reorder_threshold || 0);
                    });
                    const lowStockNames = new Set(Object.keys(thresholdByName).filter(name => (stockByName[name] || 0) <= thresholdByName[name]));

                    document.getElementById('statInvTotal').innerText = tenantItems.length;
                    document.getElementById('statInvStock').innerText = tenantItems.filter(i => i.status === 'In_Stock').length;
                    document.getElementById('statInvAssigned').innerText = tenantItems.filter(i => i.status !== 'In_Stock').length;
                    document.getElementById('statInvLowStock').innerText = lowStockNames.size;

                    const filtered = tenantItems.filter(item => {
                        if (inventoryFilter === 'all') return true;
                        if (inventoryFilter === 'low_stock') return lowStockNames.has(item.name);
                        return item.status === inventoryFilter;
                    });

                    filtered.forEach(item => {
                        const tr = document.createElement('tr');
                        tr.className = 'hover:bg-brand-600 transition';

                        const statusColor = item.status === 'In_Stock' ? 'text-emerald-400 font-semibold' : 'text-sky-400';
                        const allocText = item.assigned_to ? item.assigned_to : '<span class=\'text-slate-500\'>Unassigned</span>';
                        const lowBadge = lowStockNames.has(item.name) ? ' <i class="fa-solid fa-triangle-exclamation text-rose-400" title="Low stock"></i>' : '';

                        const assignBtn = item.status === 'In_Stock'
                            ? '<button onclick="openAllocationModal(\'' + item.id + '\')" ' + (canWriteInventory ? '' : 'disabled') + ' class="text-xs bg-brand-900 text-emerald-400 hover:bg-brand-500 border border-brand-600 rounded py-1 px-2 font-bold transition disabled:opacity-40">Allocate</button>'
                            : '<span class="text-xs text-slate-500">Allocated</span>';

                        tr.innerHTML = '<td class="py-3 px-4 font-mono">' + item.id + '</td>' +
                            '<td class="py-3 px-4">' + item.name + lowBadge + '</td>' +
                            '<td class="py-3 px-4 font-mono">' + item.serial_number + '</td>' +
                            '<td class="py-3 px-4 ' + statusColor + '">' + item.status + '</td>' +
                            '<td class="py-3 px-4 text-slate-300">' + item.region + '</td>' +
                            '<td class="py-3 px-4 text-emerald-300 font-medium">' + allocText + '</td>' +
                            '<td class="py-3 px-4 text-slate-400">' + (item.reorder_threshold || 0) + '</td>' +
                            '<td class="py-3 px-4">' + assignBtn + '</td>';
                        invBody.appendChild(tr);
                    });
                }
            }

            // Render Finance & Invoice Ledger
            const finBody = document.getElementById('financeTableBody');
            if (finBody) {
                finBody.innerHTML = '';
                const paySelect = document.getElementById('payInvoiceId');
                paySelect.innerHTML = '';

                Object.values(currentState.invoices || {}).forEach(inv => {
                    if (inv.tenant_id !== activeTenant) return;

                    const opt = document.createElement('option');
                    opt.value = inv.id;
                    opt.text = inv.id + ' (Bal: KSh ' + inv.balance_amount + ')';
                    paySelect.appendChild(opt);

                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';
                    const statusColor = inv.status === 'Paid' ? 'text-emerald-400 font-bold' : (inv.status === 'Partially_Paid' ? 'text-amber-400' : 'text-slate-300');

                    tr.innerHTML = '<td class="py-3 px-4 font-bold">' + inv.id + '</td>' +
                        '<td class="py-3 px-4 font-mono">KSh ' + inv.total_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 font-mono">KSh ' + inv.paid_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 font-mono text-rose-400">KSh ' + inv.balance_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 ' + statusColor + '">' + inv.status + '</td>';
                    finBody.appendChild(tr);
                });
            }

            // Render Material requests List & Timesheets Requisitions
            const matBody = document.getElementById('materialsTableBody');
            if (matBody) {
                matBody.innerHTML = '';

                const canApproveMaterials = activeRolePermissions.includes('tasks:approve') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');

                Object.values(currentState.material_requests || {}).forEach(mr => {
                    if (mr.tenant_id !== activeTenant) return;

                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    const statusColor = mr.status === 'Fulfilled' ? 'text-emerald-400 font-bold' : (mr.status === 'Procuring' ? 'text-amber-400 italic animate-pulse' : 'text-sky-300');

                    const actionBtn = mr.status === 'Pending_Leader_Approval'
                        ? '<button onclick="approveMaterial(\'' + mr.id + '\')" ' + (canApproveMaterials ? '' : 'disabled') + ' class="text-[10px] bg-emerald-600 hover:bg-emerald-500 font-bold text-white px-2 py-1 rounded disabled:opacity-40">Approve Request</button>'
                        : '<span class="text-slate-500 italic">Closed</span>';

                    tr.innerHTML = '<td class="py-2 px-3">' + mr.id + '</td>' +
                        '<td class="py-2 px-3">' + mr.task_id + '</td>' +
                        '<td class="py-2 px-3 text-slate-300">' + mr.requester_id + '</td>' +
                        '<td class="py-2 px-3 font-semibold">' + mr.item_name + '</td>' +
                        '<td class="py-2 px-3 ' + statusColor + '">' + mr.status + '</td>' +
                        '<td class="py-2 px-3 text-emerald-300 font-bold">' + (mr.allocated_sn || '-') + '</td>' +
                        '<td class="py-2 px-3">' + actionBtn + '</td>';
                    matBody.appendChild(tr);
                });
            }

            // Render Timesheets
            const tsBody = document.getElementById('timesheetsTableBody');
            if (tsBody) {
                tsBody.innerHTML = '';
                const canApproveTimesheets = activeRolePermissions.includes('timesheets:approve') || activeRolePermissions.includes('tasks:*') || activeRolePermissions.includes('*');
                const isTech = activeRole === 'field_technician';

                Object.values(currentState.timesheets || {}).forEach(ts => {
                    if (ts.tenant_id !== activeTenant) return;

                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    let approveBtn = ts.status === 'Submitted'
                        ? '<button onclick="approveTimesheet(\'' + ts.id + '\')" ' + (canApproveTimesheets ? '' : 'disabled') + ' class="text-xs bg-sky-600 hover:bg-sky-500 text-white font-bold py-1 px-2 rounded disabled:opacity-40">Approve</button>'
                        : '<span class="text-slate-500 text-xs">Approved</span>';

                    // If tech, let them request materials directly
                    if (isTech && ts.status === 'Submitted') {
                        approveBtn = '<button onclick="openMaterialRequestModal(\'' + ts.task_id + '\')" class="text-xs bg-amber-600 hover:bg-amber-500 text-white font-bold py-1 px-2 rounded">Requisition Router</button>';
                    }

                    tr.innerHTML = '<td class="py-3 px-4 font-mono">' + ts.id + '</td>' +
                        '<td class="py-3 px-4 font-mono">' + ts.task_id + '</td>' +
                        '<td class="py-3 px-4 text-emerald-300 font-medium">' + ts.user_id + '</td>' +
                        '<td class="py-3 px-4 font-bold">' + ts.hours + ' hrs</td>' +
                        '<td class="py-3 px-4">' + ts.status + '</td>' +
                        '<td class="py-3 px-4 text-slate-400 font-medium">' + (ts.approved_by || '-') + '</td>' +
                        '<td class="py-3 px-4">' + approveBtn + '</td>';
                    tsBody.appendChild(tr);
                });
            }

            // Render Invoice Notes (Billing Reconciliation Desk)
            const noteBody = document.getElementById('invoiceNotesTableBody');
            if (noteBody) {
                noteBody.innerHTML = '';
                const activeNotes = Object.values(currentState.invoice_notes || {}).filter(n => n.tenant_id === activeTenant);
                if (activeNotes.length === 0) {
                    noteBody.innerHTML = '<tr><td colspan="9" class="p-3 text-slate-500 italic text-center">No invoice notes issued.</td></tr>';
                } else {
                    activeNotes.forEach(n => {
                        const tr = document.createElement('tr');
                        tr.className = 'hover:bg-brand-600 transition';

                        // Dropdown selection for usage type
                        const selectHtml = '<select onchange="updateInvoiceNoteUsage(\'' + n.id + '\', this)" class="bg-brand-900 border border-brand-600 rounded px-1.5 py-1 text-[10px] text-slate-100 focus:outline-none">' +
                            '<option value="Internal" ' + (n.usage_type === 'Internal' ? 'selected' : '') + '>Internal Support</option>' +
                            '<option value="Customer_Installation" ' + (n.usage_type === 'Customer_Installation' ? 'selected' : '') + '>Customer Installation</option>' +
                            '<option value="Customer_Broken" ' + (n.usage_type === 'Customer_Broken' ? 'selected' : '') + '>Customer Broken</option>' +
                            '</select>';

                        let statusColor = 'text-slate-300';
                        if (n.status === 'Paid_Usage_Support') statusColor = 'text-emerald-400 font-semibold';
                        else if (n.status === 'Pending_Payment') statusColor = 'text-amber-400 font-bold';
                        else if (n.status === 'Cleared_Paid') statusColor = 'text-emerald-300 font-extrabold';
                        else if (n.status === 'Reconciliation_Started') statusColor = 'text-rose-400 font-bold animate-pulse';

                        tr.innerHTML = '<td class="py-2.5 px-3 font-bold text-emerald-400">' + n.id + '</td>' +
                            '<td class="py-2.5 px-3 font-semibold text-white">' + n.item_name + '</td>' +
                            '<td class="py-2.5 px-3 text-indigo-300">' + n.allocated_sn + '</td>' +
                            '<td class="py-2.5 px-3 font-mono">' + n.task_id + '</td>' +
                            '<td class="py-2.5 px-3 text-slate-300">' + n.requester_id + '</td>' +
                            '<td class="py-2.5 px-3">' + selectHtml + '</td>' +
                            '<td class="py-2.5 px-3 ' + statusColor + '">' + n.status + '</td>' +
                            '<td class="py-2.5 px-3 font-bold text-sky-400">' + (n.invoice_id || '-') + '</td>' +
                            '<td class="py-2.5 px-3 font-bold text-amber-400">' + (n.payment_id || '-') + '</td>';
                        noteBody.appendChild(tr);
                    });
                }
            }

            // Render Procurement Receipt table
            const procReceiptBody = document.getElementById('procurementReceiptTableBody');
            if (procReceiptBody) {
                procReceiptBody.innerHTML = '';
                const activeProcOrders = Object.values(currentState.procurement_orders || {}).filter(po => po.tenant_id === activeTenant);
                if (activeProcOrders.length === 0) {
                    procReceiptBody.innerHTML = '<tr><td colspan="7" class="p-3 text-slate-500 italic text-center">No active procurement orders.</td></tr>';
                } else {
                    activeProcOrders.forEach(po => {
                        const tr = document.createElement('tr');
                        tr.className = 'hover:bg-brand-600 transition';

                        let statusColor = 'text-amber-400 font-semibold';
                        let actionBtn = '<span class="text-slate-500 italic">No Action</span>';

                        if (po.status === 'Bidding') {
                            actionBtn = '<button onclick="confirmProcurementOrderReceipt(\'' + po.id + '\')" class="text-[10px] bg-amber-600 hover:bg-amber-500 font-bold text-white px-2 py-1 rounded shadow-sm">Confirm Purchase Receipt</button>';
                        } else if (po.status === 'Completed') {
                            statusColor = 'text-emerald-400 font-bold';
                            actionBtn = '<span class="text-emerald-400"><i class="fa-solid fa-check-double"></i> Receipt Confirmed</span>';
                        }

                        tr.innerHTML = '<td class="py-2.5 px-3 font-bold text-slate-300">' + po.id + '</td>' +
                            '<td class="py-2.5 px-3 font-semibold text-white">' + po.item_name + '</td>' +
                            '<td class="py-2.5 px-3 ' + statusColor + '">' + po.status + '</td>' +
                            '<td class="py-2.5 px-3 text-indigo-300">' + (po.barcode_photo_url ? '<i class="fa-solid fa-image"></i> ' + po.barcode_photo_url.substring(0, 15) + '...' : '-') + '</td>' +
                            '<td class="py-2.5 px-3 text-emerald-300 font-semibold">' + (po.status === 'Completed' ? 'Auto Extracted SN' : '-') + '</td>' +
                            '<td class="py-2.5 px-3 text-slate-400">' + (po.confirmed_by || '-') + '</td>' +
                            '<td class="py-2.5 px-3 text-right">' + actionBtn + '</td>';
                        procReceiptBody.appendChild(tr);
                    });
                }
            }

            // Render Customers CRM & Populate Support Ticket Client Dropdown
            const custBody = document.getElementById('customersTableBody');
            const ticketCustDropdown = document.getElementById('ticketCustID');
            if (custBody) {
                custBody.innerHTML = '';
                if (ticketCustDropdown) {
                    ticketCustDropdown.innerHTML = '';
                }

                Object.values(currentState.customers || {}).forEach(c => {
                    if (c.tenant_id !== activeTenant) return;

                    // Add to customer list table
                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    const dispatchBtn = c.dispatch_status === 'Pending'
                        ? '<button onclick="dispatchCustomerDevice(\'' + c.id + '\')" class="text-[10px] bg-indigo-600 hover:bg-indigo-500 text-white font-bold py-1 px-2 rounded">Complete &amp; Dispatch</button>'
                        : '<span class="text-emerald-400 font-bold"><i class="fa-solid fa-signature"></i> Sign-off Completed</span>';

                    tr.innerHTML = '<td class="py-3 px-4 font-bold">' + c.id + '</td>' +
                        '<td class="py-3 px-4">' + c.name + '</td>' +
                        '<td class="py-3 px-4 font-mono">' + c.phone + '</td>' +
                        '<td class="py-3 px-4 font-mono text-emerald-300">' + (c.device_id || '-') + '</td>' +
                        '<td class="py-3 px-4 font-mono text-amber-400">' + (c.invoice_id || '-') + '</td>' +
                        '<td class="py-3 px-4 font-semibold">' + c.dispatch_status + '</td>' +
                        '<td class="py-3 px-4 text-right">' + dispatchBtn + '</td>';
                    custBody.appendChild(tr);

                    // Add to ticket customer options dropdown list
                    if (ticketCustDropdown) {
                        const opt = document.createElement('option');
                        opt.value = c.id;
                        opt.textContent = c.name + ' (' + c.id + ')';
                        ticketCustDropdown.appendChild(opt);
                    }
                });
            }

            // Render Customer Support Tickets Table
            const ticketsBody = document.getElementById('ticketsTableBody');
            if (ticketsBody) {
                ticketsBody.innerHTML = '';
                const activeTickets = Object.values(currentState.support_tickets || {}).filter(t => t.tenant_id === activeTenant);

                if (activeTickets.length === 0) {
                    ticketsBody.innerHTML = '<tr><td colspan="7" class="p-3 text-slate-500 italic text-center">No active support tickets.</td></tr>';
                } else {
                    const isLeader = activeRole === 'tenant_admin' || activeRole === 'manager';
                    activeTickets.forEach(t => {
                        const tr = document.createElement('tr');
                        tr.className = 'hover:bg-brand-600 transition';

                        const statusColor = t.status === 'Open' ? 'text-amber-400 font-bold' : 'text-emerald-400 font-semibold';

                        let actionBtn = '<span class="text-slate-500 italic">No Action</span>';
                        if (t.status === 'Open') {
                            if (isLeader) {
                                actionBtn = '<button onclick="openConvertModal(\'' + t.id + '\', \'' + t.title.replace(/'/g, "\\'") + '\')" class="text-[10px] bg-indigo-600 hover:bg-indigo-500 font-bold text-white px-2 py-1 rounded">Convert to Task</button>';
                            } else {
                                actionBtn = '<span class="text-amber-400 italic">Awaiting Leader</span>';
                            }
                        } else {
                            actionBtn = '<span class="text-emerald-400"><i class="fa-solid fa-circle-check"></i> Converted</span>';
                        }

                        tr.innerHTML = '<td class="py-2.5 px-3 font-bold">' + t.id + '</td>' +
                            '<td class="py-2.5 px-3 font-semibold text-emerald-400">' + t.customer_id + '</td>' +
                            '<td class="py-2.5 px-3 font-bold text-white">' + t.title + '</td>' +
                            '<td class="py-2.5 px-3 text-slate-300">' + t.description + '</td>' +
                            '<td class="py-2.5 px-3 ' + statusColor + '">' + t.status + '</td>' +
                            '<td class="py-2.5 px-3 text-indigo-300 font-bold">' + (t.task_id || '-') + '</td>' +
                            '<td class="py-2.5 px-3 text-right">' + actionBtn + '</td>';
                        ticketsBody.appendChild(tr);
                    });
                }
            }

            // Render Users Personnel Key directory
            const usersBody = document.getElementById('usersTableBody');
            if (usersBody) {
                usersBody.innerHTML = '';
                const allUsers = Object.values(currentState.users || {}).filter(u => u.tenant_id === activeTenant);
                allUsers.forEach(u => {
                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    const actionBtn = '<button onclick="openPasswordResetModal(\'' + u.id + '\')" class="text-xs bg-slate-800 hover:bg-slate-700 text-slate-300 font-bold px-2 py-1.5 rounded border border-brand-600"><i class="fa-solid fa-key text-amber-400 mr-1"></i>Reset Password</button>';

                    // Manager info
                    let managerName = 'None';
                    if (u.manager_id) {
                        const mUser = currentState.users[u.manager_id];
                        if (mUser) {
                            managerName = mUser.name;
                        }
                    }

                    // Role assignment select dropdown
                    const roleFriendlyNames = {
                        'field_technician': 'Field Technician',
                        'manager': 'Reporting Manager',
                        'finance_officer': 'Finance Officer',
                        'tenant_admin': 'Tenant Administrator'
                    };
                    let roleSelect = '<select onchange="updateTeammateRole(\'' + u.id + '\', this.value)" class="bg-brand-900 border border-brand-600 rounded text-xs px-2 py-1 text-slate-200 focus:outline-none">';
                    ['field_technician', 'manager', 'finance_officer', 'tenant_admin'].forEach(r => {
                        const isSelected = r === u.role_name ? 'selected' : '';
                        roleSelect += '<option value="' + r + '" ' + isSelected + '>' + (roleFriendlyNames[r] || r) + '</option>';
                    });
                    roleSelect += '</select>';

                    tr.innerHTML = '<td class="py-3 px-4 font-bold font-mono">' + u.id + '</td>' +
                        '<td class="py-3 px-4 text-slate-300">' +
                            '<div class="font-bold">' + u.name + '</div>' +
                            '<div class="text-[10px] text-slate-400">' + u.email + '</div>' +
                            '<div class="text-[10px] text-emerald-400 mt-0.5"><i class="fa-solid fa-arrow-turn-up text-[8px] mr-1"></i>Manager: ' + managerName + '</div>' +
                        '</td>' +
                        '<td class="py-3 px-4">' + roleSelect + '</td>' +
                        '<td class="py-3 px-4 text-slate-400">' + u.region + '</td>' +
                        '<td class="py-3 px-4 text-right">' + actionBtn + '</td>';
                    usersBody.appendChild(tr);
                });
            }

            // Render Pending invitations
            const inviteTableBody = document.getElementById('invitationsTableBody');
            if (inviteTableBody) {
                inviteTableBody.innerHTML = '';
                const activeInvitations = Object.values(currentState.invitations || {}).filter(inv => inv.tenant_id === activeTenant);
                if (activeInvitations.length === 0) {
                    inviteTableBody.innerHTML = '<tr><td colspan="6" class="p-3 text-slate-500 italic text-center">No pending invitations found.</td></tr>';
                } else {
                    activeInvitations.forEach(inv => {
                        const tr = document.createElement('tr');
                        tr.className = 'hover:bg-brand-600 transition';

                        const statusColor = inv.status === 'Pending' ? 'text-amber-400' : 'text-emerald-400';
                        const activationUrl = window.location.origin + '/activate.html?token=' + inv.token + '&tenant_id=' + inv.tenant_id + '&role_assignment_id=' + inv.role_name;

                        const roleFriendlyNames = {
                            'field_technician': 'Field Technician',
                            'manager': 'Reporting Manager',
                            'finance_officer': 'Finance Officer',
                            'tenant_admin': 'Tenant Administrator'
                        };
                        tr.innerHTML = '<td class="py-2 px-3">' + inv.email + '</td>' +
                            '<td class="py-2 px-3 font-semibold text-white">' + inv.name + '</td>' +
                            '<td class="py-2 px-3"><span class="px-1.5 py-0.5 bg-brand-900 border border-brand-600 rounded text-[10px]">' + (roleFriendlyNames[inv.role_name] || inv.role_name) + '</span></td>' +
                            '<td class="py-2 px-3">' + inv.region + '</td>' +
                            '<td class="py-2 px-3 ' + statusColor + '">' + inv.status + '</td>' +
                            '<td class="py-2 px-3 text-right">' +
                                '<button onclick="navigator.clipboard.writeText(\'' + activationUrl + '\'); alert(\'Copied activation link!\')" class="text-[10px] bg-brand-900 text-emerald-400 hover:bg-brand-600 border border-brand-600 rounded py-1 px-2 font-bold transition">Copy Link</button>' +
                            '</td>';
                        inviteTableBody.appendChild(tr);
                    });
                }
            }

            // Render Visual Tree Organogram
            renderOrgTree();
        }

        async function createInventoryItem(e) {
            e.preventDefault();
            const name = document.getElementById('itemName').value;
            const sn = document.getElementById('itemSerial').value;
            const reorderThreshold = parseInt(document.getElementById('itemReorderThreshold').value, 10) || 0;

            try {
                const res = await fetch('/api/inventory', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ name, serial_number: sn, reorder_threshold: reorderThreshold })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('INVENTORY_QUEUED', 'Creation command published to SALES stream.');
                    document.getElementById('itemName').value = '';
                    document.getElementById('itemSerial').value = '';
                    document.getElementById('itemReorderThreshold').value = '0';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed item create:', err);
            }
        }

        async function recordPayment(e) {
            e.preventDefault();
            const invoiceID = document.getElementById('payInvoiceId').value;
            const amount = parseFloat(document.getElementById('payAmount').value);
            const ref = document.getElementById('payRef').value;

            try {
                const res = await fetch('/api/finance/payments', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ invoice_id: invoiceID, amount, payment_method: 'Mpesa_Paybill', reference: ref })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('MPESA_QUEUED', 'M-Pesa payment published to FINANCE bus.');
                    document.getElementById('payAmount').value = '';
                    document.getElementById('payRef').value = '';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed payment recording:', err);
            }
        }

        async function createCustomer(e) {
            e.preventDefault();
            const id = document.getElementById('custID').value;
            const name = document.getElementById('custName').value;
            const phone = document.getElementById('custPhone').value;

            try {
                const res = await fetch('/api/customers', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ id, name, phone, email: id + '@gmail.com' })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('CUSTOMER_QUEUED', 'Client creation command published successfully.');
                    document.getElementById('custID').value = '';
                    document.getElementById('custName').value = '';
                    document.getElementById('custPhone').value = '';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed customer create:', err);
            }
        }

        async function approveTimesheet(timesheetId) {
            try {
                const res = await fetch('/api/tasks/timesheets/approve', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ timesheet_id: timesheetId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('TIMESHEET_QUEUED', 'Approval of timesheet ' + timesheetId + ' published to NATS.');
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Authorization Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed timesheet approval:', err);
            }
        }

        function openMaterialRequestModal(taskId) {
            // Fill select
            const select = document.getElementById('matModalTaskId');
            select.innerHTML = '<option value="' + taskId + '">' + taskId + '</option>';
            document.getElementById('materialRequestModal').classList.remove('hidden');
        }
        function closeMaterialRequestModal() {
            document.getElementById('materialRequestModal').classList.add('hidden');
        }
        async function submitMaterialRequest() {
            const taskId = document.getElementById('matModalTaskId').value;
            const item = document.getElementById('matModalItemName').value;

            try {
                const res = await fetch('/api/tasks/materials/request', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ task_id: taskId, item_name: item })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('MATERIAL_REQUESTED', 'Requisition command for task ' + taskId + ' queued.');
                    closeMaterialRequestModal();
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function approveMaterial(requestId) {
            try {
                const res = await fetch('/api/tasks/materials/approve', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ request_id: requestId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('MATERIAL_APPROVED', 'Material request approval command dispatched.');
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        function openPasswordResetModal(userId) {
            document.getElementById('resetModalUserId').value = userId;
            document.getElementById('passwordResetModal').classList.remove('hidden');
        }
        function closePasswordResetModal() {
            document.getElementById('passwordResetModal').classList.add('hidden');
        }
        async function submitPasswordReset() {
            const userId = document.getElementById('resetModalUserId').value;
            const pass = document.getElementById('resetModalNewPassword').value;

            if (!pass) {
                alert('Please enter a valid password.');
                return;
            }

            try {
                const res = await fetch('/api/users/reset-password', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ user_id: userId, new_password: pass })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('PASSWORD_RESET', 'Credential keys update command published.');
                    closePasswordResetModal();
                    document.getElementById('resetModalNewPassword').value = '';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        function openAllocationModal(itemId) {
            document.getElementById('modalItemId').value = itemId;
            const techSelect = document.getElementById('modalTechId');
            techSelect.innerHTML = '';
            const currentTenant = currentUser.tenant_id;

            Object.values(currentState.users || {}).forEach(u => {
                if (u.tenant_id === currentTenant && u.role_name === 'field_technician') {
                    const opt = document.createElement('option');
                    opt.value = u.id;
                    opt.text = u.name + ' (Region: ' + u.region + ')';
                    techSelect.appendChild(opt);
                }
            });

            document.getElementById('allocationModal').classList.remove('hidden');
        }

        function closeModal() {
            document.getElementById('allocationModal').classList.add('hidden');
        }

        async function submitAllocation() {
            const itemId = document.getElementById('modalItemId').value;
            const techId = document.getElementById('modalTechId').value;

            try {
                const res = await fetch('/api/inventory/assign', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ item_id: itemId, user_id: techId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('ASSIGN_QUEUED', 'Device allocation command published successfully.');
                    closeModal();
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed assign device:', err);
            }
        }

        async function dispatchCustomerDevice(customerId) {
            // Pick a matching available device serial (mock dispatch assignment)
            let deviceId = "item_onu_1";
            let invoiceId = "inv_safari_1";

            const techObj = Object.values(currentState.users || {}).find(u => u.tenant_id === currentUser.tenant_id && u.role_name === 'field_technician');

            // Assign a device serial number to customer, capture signature, and set status to dispatched
            alert("Customer signature captured: 'I accept drop-cable installation and router ONT serial configuration HW-GPON-9901.'");
            pushNotification('CLIENT_SIGNOFF_DISPATCHED', 'Client completed. Customer digital signature logged.');

            currentState.customers[customerId].device_id = deviceId;
            currentState.customers[customerId].dispatch_status = "Dispatched";
            fetchState();
        }

        async function createSupportTicket(e) {
            e.preventDefault();
            const customer_id = document.getElementById('ticketCustID').value;
            const title = document.getElementById('ticketTitle').value;
            const description = document.getElementById('ticketDesc').value;

            try {
                const res = await fetch('/api/tickets', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ customer_id, title, description })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('TICKET_CREATED', 'Support ticket ' + r.id + ' successfully registered.');
                    document.getElementById('ticketTitle').value = '';
                    document.getElementById('ticketDesc').value = '';
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        function openConvertModal(ticketId, ticketTitle) {
            document.getElementById('convertTicketId').value = ticketId;
            document.getElementById('convertTaskTitle').value = 'Troubleshoot: ' + ticketTitle;

            const activeTenant = currentUser.tenant_id;

            // Populate assignee dropdown options with technicians and managers under active tenant
            const assignSelect = document.getElementById('convertAssignee');
            if (assignSelect) {
                assignSelect.innerHTML = '';
                Object.values(currentState.users || {}).forEach(u => {
                    if (u.tenant_id === activeTenant) {
                        const opt = document.createElement('option');
                        opt.value = u.id;
                        opt.textContent = u.name + ' (' + u.role_name + ')';
                        assignSelect.appendChild(opt);
                    }
                });
            }

            // Populate dependency dropdown list with other existing tasks
            const depSelect = document.getElementById('convertDependency');
            if (depSelect) {
                depSelect.innerHTML = '<option value="">No Dependency (Independent)</option>';
                Object.values(currentState.tasks || {}).forEach(t => {
                    if (t.tenant_id === activeTenant) {
                        const opt = document.createElement('option');
                        opt.value = t.id;
                        opt.textContent = t.title + ' (Status: ' + t.status + ')';
                        depSelect.appendChild(opt);
                    }
                });
            }

            document.getElementById('convertTicketModal').classList.remove('hidden');
        }

        function closeConvertModal() {
            document.getElementById('convertTicketModal').classList.add('hidden');
            document.getElementById('convertForm').reset();
        }

        async function handleConvertTicket(e) {
            e.preventDefault();
            const ticket_id = document.getElementById('convertTicketId').value;
            const title = document.getElementById('convertTaskTitle').value;
            const assigned_to = document.getElementById('convertAssignee').value;
            const depends_on = document.getElementById('convertDependency').value;

            try {
                const res = await fetch('/api/tickets/convert', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ ticket_id, title, assigned_to, depends_on })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('TICKET_CONVERTED', 'Ticket successfully converted into Task ' + r.task_id);
                    closeConvertModal();
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function addManualInventoryItem(e) {
            e.preventDefault();
            const name = document.getElementById('manualItemName').value;
            const serial_number = document.getElementById('manualItemSerial').value;
            const reorder_threshold = parseInt(document.getElementById('manualItemThreshold').value, 10) || 0;
            const region = document.getElementById('manualItemRegion').value;

            try {
                const res = await fetch('/api/inventory/add', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ name, serial_number, reorder_threshold, region })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('MANUAL_ITEM_ADDED', 'Serialized asset manually registered directly.');
                    document.getElementById('manualItemName').value = '';
                    document.getElementById('manualItemSerial').value = '';
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function updateInvoiceNoteUsage(noteId, selectEl) {
            const usage_type = selectEl.value;
            // Ask for customer selection if installation/broken
            let customer_id = '';
            if (usage_type !== 'Internal') {
                customer_id = prompt('Enter Customer ID for this installation invoice (e.g. cust_saf_77):', 'cust_saf_77');
                if (!customer_id) {
                    selectEl.value = 'Internal';
                    return;
                }
            }

            try {
                const res = await fetch('/api/materials/invoice-note/update', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ note_id: noteId, usage_type, customer_id })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('INVOICE_NOTE_UPDATED', 'Invoice note usage updated to ' + usage_type);
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function confirmProcurementOrderReceipt(procId) {
            const barcode_photo_url = prompt('Provide Barcode Photo URL for automatic scanner processing:', 'https://imgur.com/barcode_router_sn.png');
            if (!barcode_photo_url) return;

            try {
                const res = await fetch('/api/procurement/confirm', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ proc_id: procId, barcode_photo_url })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('PROCUREMENT_CONFIRMED', r.message);
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        function switchUsersTab(tab) {
            const directoryBtn = document.getElementById('usersTab_directory');
            const treeBtn = document.getElementById('usersTab_tree');
            const directoryPanel = document.getElementById('usersPanel_directory');
            const treePanel = document.getElementById('usersPanel_tree');

            if (tab === 'directory') {
                directoryBtn.className = 'text-sm font-bold text-emerald-400 border-b-2 border-emerald-400 pb-2 flex items-center gap-2';
                treeBtn.className = 'text-sm font-bold text-slate-400 hover:text-slate-200 pb-2 flex items-center gap-2';
                directoryPanel.classList.remove('hidden');
                treePanel.classList.add('hidden');
            } else {
                treeBtn.className = 'text-sm font-bold text-emerald-400 border-b-2 border-emerald-400 pb-2 flex items-center gap-2';
                directoryBtn.className = 'text-sm font-bold text-slate-400 hover:text-slate-200 pb-2 flex items-center gap-2';
                treePanel.classList.remove('hidden');
                directoryPanel.classList.add('hidden');
                renderOrgTree();
            }
        }

        function openInviteModal() {
            const select = document.getElementById('inviteManager');
            select.innerHTML = '<option value="">No Reporting Manager (Root)</option>';
            const activeTenant = currentUser.tenant_id;

            Object.values(currentState.users || {}).forEach(u => {
                if (u.tenant_id === activeTenant && (u.role_name === 'manager' || u.role_name === 'tenant_admin')) {
                    const opt = document.createElement('option');
                    opt.value = u.id;
                    opt.textContent = u.name + ' (' + u.role_name + ')';
                    select.appendChild(opt);
                }
            });
            document.getElementById('inviteModal').classList.remove('hidden');
        }

        function closeInviteModal() {
            document.getElementById('inviteModal').classList.add('hidden');
            document.getElementById('inviteForm').reset();
        }

        async function handleSendInvitation(e) {
            e.preventDefault();
            const name = document.getElementById('inviteName').value;
            const email = document.getElementById('inviteEmail').value;
            const region = document.getElementById('inviteRegion').value;
            const manager_id = document.getElementById('inviteManager').value;
            const role_name = document.querySelector('input[name="inviteRole"]:checked').value;

            try {
                const res = await fetch('/api/users/invite', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ name, email, region, manager_id, role_name })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('INVITATION_SENT', 'Teammate workspace invitation dispatched for ' + email);
                    closeInviteModal();
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function updateTeammateManager(userId, managerId) {
            try {
                const res = await fetch('/api/users/update-manager', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ user_id: userId, manager_id: managerId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('HIERARCHY_MUTATED', 'Teammate reporting relationship updated.');
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function updateTeammateRole(userId, roleName) {
            try {
                const res = await fetch('/api/users/update-role', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ user_id: userId, role_name: roleName })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('ROLE_ASSIGNMENT_MUTATED', 'Teammate role assignment updated.');
                    fetchState();
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error(err);
            }
        }

        function renderOrgTree() {
            const activeTenant = currentUser.tenant_id;
            const allUsers = Object.values(currentState.users || {}).filter(u => u.tenant_id === activeTenant);

            // Group users by manager_id
            const usersByManager = {};
            const rootUsers = [];

            allUsers.forEach(u => {
                if (!u.manager_id) {
                    rootUsers.push(u);
                } else {
                    if (!usersByManager[u.manager_id]) {
                        usersByManager[u.manager_id] = [];
                    }
                    usersByManager[u.manager_id].push(u);
                }
            });

            // Recursively build tree nodes HTML
            function buildNodeHtml(user, level = 0) {
                const reports = usersByManager[user.id] || [];
                const paddingLeft = level * 24;

                let selectManagerOptions = '<option value="">No Manager (Root)</option>';
                allUsers.forEach(otherUser => {
                    if (otherUser.id !== user.id) {
                        const isSelected = otherUser.id === user.manager_id ? 'selected' : '';
                        selectManagerOptions += '<option value="' + otherUser.id + '" ' + isSelected + '>' + otherUser.name + ' (' + otherUser.role_name + ')</option>';
                    }
                });

                let selectRoleOptions = '';
                ['field_technician', 'manager', 'finance_officer', 'tenant_admin'].forEach(r => {
                    const isSelected = r === user.role_name ? 'selected' : '';
                    selectRoleOptions += '<option value="' + r + '" ' + isSelected + '>' + r + '</option>';
                });

                let nodeHtml = '<div class="flex flex-col space-y-2 border-l-2 border-emerald-500/30 pl-4 py-2 ml-4">' +
                    '<div class="bg-brand-500 p-4 rounded-xl border border-brand-600 shadow-md flex flex-col md:flex-row md:items-center justify-between gap-4 max-w-xl">' +
                        '<div class="flex items-center space-x-3">' +
                            '<div class="w-10 h-10 rounded-full bg-emerald-600 flex items-center justify-center text-white font-bold">' +
                                user.name.charAt(0) +
                            '</div>' +
                            '<div>' +
                                '<h4 class="font-bold text-sm text-white">' + user.name + '</h4>' +
                                '<p class="text-[10px] text-slate-400">' + user.email + '</p>' +
                                '<p class="text-[10px] text-emerald-400 font-mono mt-0.5">' + user.region + '</p>' +
                            '</div>' +
                        '</div>' +
                        '<div class="flex flex-col sm:flex-row gap-2">' +
                            '<div>' +
                                '<label class="block text-[8px] uppercase tracking-wider text-slate-400 mb-0.5">Manager</label>' +
                                '<select onchange="updateTeammateManager(\'' + user.id + '\', this.value)" class="bg-brand-900 border border-brand-600 rounded text-[10px] px-2 py-1 text-slate-200 focus:outline-none w-full sm:w-36">' +
                                    selectManagerOptions +
                                '</select>' +
                            '</div>' +
                            '<div>' +
                                '<label class="block text-[8px] uppercase tracking-wider text-slate-400 mb-0.5">Role</label>' +
                                '<select onchange="updateTeammateRole(\'' + user.id + '\', this.value)" class="bg-brand-900 border border-brand-600 rounded text-[10px] px-2 py-1 text-slate-200 focus:outline-none w-full sm:w-32">' +
                                    selectRoleOptions +
                                '</select>' +
                            '</div>' +
                        '</div>' +
                    '</div>';

                if (reports.length > 0) {
                    nodeHtml += '<div class="space-y-2 mt-2">';
                    reports.forEach(child => {
                        nodeHtml += buildNodeHtml(child, level + 1);
                    });
                    nodeHtml += '</div>';
                }

                nodeHtml += '</div>';
                return nodeHtml;
            }

            const container = document.getElementById('orgTreeContainer');
            if (container) {
                container.innerHTML = '';

                if (rootUsers.length === 0 && allUsers.length > 0) {
                    // If there's a loop or somehow no root, fallback to first user as root
                    rootUsers.push(allUsers[0]);
                }

                if (allUsers.length === 0) {
                    container.innerHTML = '<div class="text-slate-400 italic">No workspace teammates available to build structure.</div>';
                    return;
                }

                rootUsers.forEach(root => {
                    container.innerHTML += buildNodeHtml(root);
                });
            }
        }

        setInterval(() => {
            fetchState();
            fetchLogs();
        }, 5000);

        initAuth();
    </script>
</body>
</html>
`

const activationHtmlContent = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SME Kenya ERP - Activate Account</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <link href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.0.0/css/all.min.css" rel="stylesheet">
    <script>
        tailwind.config = {
            theme: {
                extend: {
                    colors: {
                        brand: {
                            50: '#f5f7f6',
                            100: '#e1e7e4',
                            500: '#2f4f4f',
                            600: '#243e3e',
                            900: '#142222',
                        }
                    }
                }
            }
        }
    </script>
</head>
<body class="bg-brand-900 text-slate-100 min-h-screen flex items-center justify-center p-4">
    <div class="max-w-md w-full bg-brand-500 rounded-2xl border border-brand-600 p-6 md:p-8 shadow-2xl space-y-6">
        <div class="text-center space-y-2">
            <i class="fa-solid fa-user-shield text-emerald-400 text-5xl"></i>
            <h2 class="text-2xl font-bold tracking-wide text-white">Activate ERP Account</h2>
            <p class="text-sm text-slate-300">Set up your password to complete registration</p>
        </div>

        <!-- Preview Card -->
        <div id="previewCard" class="bg-brand-900/60 p-4 rounded-xl border border-brand-600 space-y-2 text-sm">
            <div class="flex justify-between border-b border-brand-600/50 pb-2">
                <span class="text-slate-400">Teammate:</span>
                <span id="previewName" class="font-bold text-white">Loading...</span>
            </div>
            <div class="flex justify-between border-b border-brand-600/50 pb-2">
                <span class="text-slate-400">Workspace Tenant:</span>
                <span id="previewTenant" class="font-bold text-emerald-400">Loading...</span>
            </div>
            <div class="flex justify-between">
                <span class="text-slate-400">Assigned Role:</span>
                <span id="previewRole" class="font-bold text-amber-400 bg-brand-500 px-1.5 py-0.5 rounded text-xs font-mono">Loading...</span>
            </div>
        </div>

        <form onsubmit="handleActivation(event)" class="space-y-4">
            <div>
                <label class="block text-xs font-bold text-slate-400 mb-1">Choose Password (min. 8 chars)</label>
                <input type="password" id="actPassword" placeholder="••••••••" required minlength="8" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-emerald-400" oninput="validatePasswordStrength('actPassword', 'actPass')">
                <div id="actPassChecklist" class="mt-2 p-2 bg-brand-950/40 rounded border border-brand-600/30 space-y-1 text-[11px]">
                    <div id="actPassLength"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">At least 8 characters</span></div>
                    <div id="actPassUpper"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One uppercase letter</span></div>
                    <div id="actPassLower"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One lowercase letter</span></div>
                    <div id="actPassDigit"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One digit (0-9)</span></div>
                    <div id="actPassSpecial"><i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">One special character (!@#$%^&* etc.)</span></div>
                </div>
            </div>
            <div>
                <label class="block text-xs font-bold text-slate-400 mb-1">Confirm Password</label>
                <input type="password" id="actConfirmPassword" placeholder="••••••••" required minlength="8" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-1 focus:ring-emerald-400">
            </div>
            <div id="errorMsg" class="hidden text-xs text-rose-400 font-semibold"></div>
            <div id="successMsg" class="hidden text-xs text-emerald-400 font-semibold flex items-center gap-1">
                <i class="fa-solid fa-circle-check animate-bounce"></i>
                <span>Account activated! Redirecting to workspace...</span>
            </div>

            <button type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white font-bold py-3 rounded-lg transition duration-200 shadow-md">
                Activate &amp; Enter Workspace
            </button>
        </form>
    </div>

    <script>
        function validatePasswordStrength(inputId, prefix) {
            const val = document.getElementById(inputId).value;
            const criteria = {
                Length: { ok: val.length >= 8, txt: "At least 8 characters" },
                Upper: { ok: /[A-Z]/.test(val), txt: "One uppercase letter" },
                Lower: { ok: /[a-z]/.test(val), txt: "One lowercase letter" },
                Digit: { ok: /[0-9]/.test(val), txt: "One digit (0-9)" },
                Special: { ok: /[^A-Za-z0-9]/.test(val), txt: "One special character (!@#$%^&* etc.)" }
            };

            for (const key in criteria) {
                const el = document.getElementById(prefix + key);
                if (!el) continue;
                const item = criteria[key];
                if (item.ok) {
                    el.innerHTML = '<i class="fa-solid fa-circle-check text-emerald-400"></i> <span class="text-emerald-400">' + item.txt + '</span>';
                } else {
                    el.innerHTML = '<i class="fa-solid fa-circle-xmark text-rose-500"></i> <span class="text-slate-400">' + item.txt + '</span>';
                }
            }
        }

        const params = new URLSearchParams(window.location.search);
        const token = params.get('token');
        const urlTenant = params.get('tenant_id');
        const urlRole = params.get('role_assignment_id');

        const roleFriendlyNames = {
            'field_technician': 'Field Technician',
            'manager': 'Reporting Manager',
            'finance_officer': 'Finance Officer',
            'tenant_admin': 'Tenant Administrator'
        };

        async function loadInvitationPreview() {
            if (!token) {
                showError("No activation token provided. Please check your invitation email.");
                return;
            }

            // Fill standard values from URL as quick fallback
            document.getElementById('previewTenant').textContent = urlTenant || "SME Tenant";
            document.getElementById('previewRole').textContent = roleFriendlyNames[urlRole] || urlRole || "Field Technician";

            try {
                const res = await fetch('/api/invite/preview?token=' + encodeURIComponent(token));
                if (res.ok) {
                    const data = await res.json();
                    document.getElementById('previewName').textContent = data.name;
                    document.getElementById('previewTenant').textContent = data.tenant_id;
                    document.getElementById('previewRole').textContent = roleFriendlyNames[data.role_name] || data.role_name;
                } else {
                    document.getElementById('previewName').textContent = "Invited Colleague";
                }
            } catch (err) {
                document.getElementById('previewName').textContent = "Invited Colleague";
            }
        }

        function showError(msg) {
            const el = document.getElementById('errorMsg');
            el.textContent = msg;
            el.classList.remove('hidden');
        }

        async function handleActivation(e) {
            e.preventDefault();
            document.getElementById('errorMsg').classList.add('hidden');
            document.getElementById('successMsg').classList.add('hidden');

            const password = document.getElementById('actPassword').value;
            const confirm = document.getElementById('actConfirmPassword').value;

            if (password !== confirm) {
                showError("Passwords do not match!");
                return;
            }

            try {
                const res = await fetch('/api/activate', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ token, password })
                });
                const data = await res.json();
                if (!res.ok) {
                    showError(data.error || "Activation failed. The token may have expired or already been accepted.");
                    return;
                }

                // Store session token and redirect immediately to home dashboard
                sessionStorage.setItem('erp_token', data.token);
                document.getElementById('successMsg').classList.remove('hidden');
                setTimeout(() => {
                    window.location.href = '/';
                }, 1500);

            } catch (err) {
                showError("Network error. Please try again.");
            }
        }

        loadInvitationPreview();
    </script>
</body>
</html>
`
