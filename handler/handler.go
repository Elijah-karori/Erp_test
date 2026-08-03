package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"

	"erp-event-bus/auth"
	"erp-event-bus/db"
	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

// ERPHandler publishes tenant, inventory, finance and task messages to JetStream
type ERPHandler struct {
	nc       *nats.Conn
	js       nats.JetStreamContext
	sqliteDB *db.SQLiteDB
	db       *db.Database
}

// NewERPHandler constructs a new ERPHandler with SQLite logging
func NewERPHandler(nc *nats.Conn, js nats.JetStreamContext, sdb *db.SQLiteDB, database *db.Database) *ERPHandler {
	return &ERPHandler{nc: nc, js: js, sqliteDB: sdb, db: database}
}

// Helper to construct a NATS message with multi-tenant context propagation headers
func (h *ERPHandler) newContextMsg(subject string, c echo.Context) *nats.Msg {
	msg := nats.NewMsg(subject)
	msg.Header.Set(types.HeaderTenantID, c.Get(middleware.ContextTenantID).(string))
	msg.Header.Set(types.HeaderUserID, c.Get(middleware.ContextUserID).(string))
	msg.Header.Set(types.HeaderUserRoles, c.Get(middleware.ContextRoles).(string))
	msg.Header.Set(types.HeaderUserRegion, c.Get(middleware.ContextRegion).(string))
	msg.Header.Set(types.HeaderTraceID, uuid.New().String())
	return msg
}

// CreateInventoryItemHandler publishes a command to the INVENTORY stream (erp.inventory.item.cmd.create)
func (h *ERPHandler) CreateInventoryItemHandler(c echo.Context) error {
	var cmd types.CreateItemCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.SerialNumber == "" || cmd.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Name and SerialNumber are required"})
	}

	msg := h.newContextMsg("erp.inventory.item.cmd.create", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish item create command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "CreateItem_Request", fmt.Sprintf("Queued creation of %s (SN: %s)", cmd.Name, cmd.SerialNumber))

	go h.db.CheckAndTriggerReorder(tenantID, cmd.Name)

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Item creation queued"})
}

// AssignDeviceHandler publishes device allocation command to erp.inventory.item.cmd.assign
func (h *ERPHandler) AssignDeviceHandler(c echo.Context) error {
	var cmd types.AssignDeviceCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.ItemID == "" || cmd.UserID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "ItemID and UserID are required"})
	}

	msg := h.newContextMsg("erp.inventory.item.cmd.assign", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish device assignment command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "AssignItem_Request", fmt.Sprintf("Queued allocation of asset %s to tech %s", cmd.ItemID, cmd.UserID))

	if item, err := h.db.GetInventoryItem(cmd.ItemID); err == nil {
		go h.db.CheckAndTriggerReorder(tenantID, item.Name)
	}

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Device assignment queued"})
}

// RecordPaymentHandler (M-Pesa reconciliation, etc) publishes to erp.finance.payment.cmd.record
func (h *ERPHandler) RecordPaymentHandler(c echo.Context) error {
	var cmd types.RecordPaymentCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.InvoiceID == "" || cmd.Amount <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "InvoiceID and positive Amount required"})
	}

	msg := h.newContextMsg("erp.finance.payment.cmd.record", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish payment command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "RecordPayment_Request", fmt.Sprintf("Queued payment of KSh %.2f on invoice %s (Ref: %s)", cmd.Amount, cmd.InvoiceID, cmd.Reference))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Payment recording queued"})
}

// ApproveTimesheetHandler publishes to erp.tasks.timesheet.cmd.approve
func (h *ERPHandler) ApproveTimesheetHandler(c echo.Context) error {
	var cmd types.ApproveTimesheetCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.TimesheetID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "TimesheetID is required"})
	}

	// Retrieve details synchronously to perform synchronous region ABAC/tenant checks
	ts, err := h.db.GetTimesheet(cmd.TimesheetID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Timesheet not found"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	if ts.TenantID != tenantID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Forbidden: Tenant boundary breach"})
	}

	task, err := h.db.GetTask(ts.TaskID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Task not found"})
	}

	userID := c.Get(middleware.ContextUserID).(string)
	user, err := h.db.GetUser(userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to resolve user context"})
	}

	if user.Region != task.Region {
		h.sqliteDB.Log(tenantID, userID, "SecurityViolation_RegionMismatch", fmt.Sprintf("Synchronous block: User tried to approve timesheet %s with region mismatch", cmd.TimesheetID))
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Forbidden: Region mismatch"})
	}

	msg := h.newContextMsg("erp.tasks.timesheet.cmd.approve", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err = h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish timesheet approval command"})
	}

	h.sqliteDB.Log(tenantID, userID, "ApproveTimesheet_Request", fmt.Sprintf("Queued approval of timesheet %s", cmd.TimesheetID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Timesheet approval queued"})
}

// ResetPasswordHandler publishes password resets to NATS
func (h *ERPHandler) ResetPasswordHandler(c echo.Context) error {
	var cmd types.ResetPasswordCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.UserID == "" || cmd.NewPassword == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "UserID and NewPassword are required"})
	}

	// Hash here, synchronously, before anything touches the event bus —
	// NATS payloads land in JetStream storage and the SQLite audit log, so
	// the plaintext password must never survive past this point.
	hash, err := auth.HashPassword(cmd.NewPassword)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	cmd.NewPassword = hash

	msg := h.newContextMsg("erp.users.auth.cmd.reset_password", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err = h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish reset password command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "ResetPassword_Request", fmt.Sprintf("Queued credential reset for user %s", cmd.UserID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Password reset queued"})
}

// SubmitMaterialRequestHandler registers a technician material requisition
func (h *ERPHandler) SubmitMaterialRequestHandler(c echo.Context) error {
	var cmd types.SubmitMaterialRequestCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.TaskID == "" || cmd.ItemName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "TaskID and ItemName are required"})
	}

	msg := h.newContextMsg("erp.tasks.materials.cmd.request", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish material request command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "MaterialRequest_Request", fmt.Sprintf("Queued material request of '%s' for task %s", cmd.ItemName, cmd.TaskID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Material request queued"})
}

// ApproveMaterialHandler handles team lead approvals
func (h *ERPHandler) ApproveMaterialHandler(c echo.Context) error {
	var cmd types.ApproveMaterialCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.RequestID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "RequestID is required"})
	}

	msg := h.newContextMsg("erp.tasks.materials.cmd.approve", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish material approval command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "MaterialApproval_Request", fmt.Sprintf("Queued approval of material request %s", cmd.RequestID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Material approval queued"})
}

// CreateCustomerHandler registers customers dynamically
func (h *ERPHandler) CreateCustomerHandler(c echo.Context) error {
	var cmd types.CreateCustomerCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.ID == "" || cmd.Name == "" || cmd.Phone == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "ID, Name, and Phone are required"})
	}

	msg := h.newContextMsg("erp.customers.crm.cmd.create", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish customer create command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "CreateCustomer_Request", fmt.Sprintf("Queued registration of customer %s (%s)", cmd.Name, cmd.Phone))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Customer creation queued"})
}
