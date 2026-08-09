package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

	tenantID := c.Get(middleware.ContextTenantID).(string)
	exists, err := h.db.CheckSerialNumberExists(tenantID, cmd.SerialNumber)
	if err == nil && exists {
		return c.JSON(http.StatusConflict, map[string]string{"error": "serial number already registered"})
	}

	msg := h.newContextMsg("erp.inventory.item.cmd.create", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err = h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish item create command"})
	}

	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "CreateItem_Request", fmt.Sprintf("Queued creation of %s (SN: %s)", cmd.Name, cmd.SerialNumber))

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
	if err := c.Bind(&cmd); err != nil || cmd.TimesheetID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "TimesheetID is required"})
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	region, _ := c.Get(middleware.ContextRegion).(string)
	ts, err := h.db.GetTimesheet(cmd.TimesheetID)
	if err != nil || ts.TenantID != tenantID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "timesheet not found"})
	}
	task, err := h.db.GetTask(ts.TaskID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "associated task not found"})
	}
	if task.Region != region {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "unauthorized: task region mismatch"})
	}

	id, err := h.createWorkflow(c, "TIMESHEET", cmd.TimesheetID, "TIMESHEET_APPROVAL", "erp.tasks.timesheet.cmd.approve", cmd)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create timesheet workflow"})
	}
	return c.JSON(http.StatusAccepted, map[string]string{
		"message":     "Timesheet submitted to governance queue; approval is required before execution",
		"workflow_id": id,
		"status":      "REQUESTED",
	})
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
	cmd.RequestID = "req_" + uuid.NewString()[:8]
	cmd.RequesterID = c.Get(middleware.ContextUserID).(string)

	id, err := h.createWorkflow(c, "MATERIAL_REQUEST", cmd.RequestID, "MATERIAL_FULFILLMENT", "erp.tasks.materials.cmd.fulfill", cmd)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create material request workflow"})
	}
	return c.JSON(http.StatusAccepted, map[string]string{
		"message":     "Material request submitted for governed fulfillment",
		"workflow_id": id,
		"request_id":  cmd.RequestID,
		"status":      "REQUESTED",
	})
}

// ApproveMaterialHandler is retained as a compatibility endpoint. It now performs
// the governance APPROVE transition; it never publishes the fulfillment command.
func (h *ERPHandler) ApproveMaterialHandler(c echo.Context) error {
	var cmd types.ApproveMaterialCommand
	if err := c.Bind(&cmd); err != nil || cmd.RequestID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "RequestID is required"})
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	var instanceID, status string
	err := h.db.Pool.QueryRow(c.Request().Context(), `
		SELECT id,status FROM workflow_instances
		WHERE tenant_id=$1 AND entity_type='MATERIAL_REQUEST' AND entity_id=$2
		ORDER BY created_at DESC LIMIT 1`, tenantID, cmd.RequestID).Scan(&instanceID, &status)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "material workflow not found"})
	}
	if status != "REQUESTED" && status != "RETURNED" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "material workflow is not awaiting approval"})
	}
	d := types.WorkflowDecision{InstanceID: instanceID, Action: "approve"}
	body, _ := json.Marshal(d)
	c.SetPath("/api/workflows/decision")
	c.SetParamNames("instance_id")
	c.SetParamValues(instanceID)
	_ = body
	// Reuse the same governance transition without duplicating policy.
	var requested string
	_ = h.db.Pool.QueryRow(c.Request().Context(), `SELECT requested_by FROM workflow_instances WHERE id=$1`, instanceID).Scan(&requested)
	actor := c.Get(middleware.ContextUserID).(string)
	if requested == actor {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "segregation of duties: requester cannot approve own material request"})
	}
	_, err = h.db.Pool.Exec(c.Request().Context(), `UPDATE workflow_instances SET status='APPROVED',checked_by=$1,updated_at=now() WHERE id=$2 AND tenant_id=$3`, actor, instanceID, tenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "workflow approval failed"})
	}
	_, err = h.db.Pool.Exec(c.Request().Context(), `INSERT INTO workflow_decisions(id,instance_id,tenant_id,actor_id,action) VALUES($1,$2,$3,$4,'approve')`, uuid.NewString(), instanceID, tenantID, actor)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "workflow audit write failed"})
	}
	return c.JSON(http.StatusAccepted, map[string]string{"message": "Material workflow approved; execution is required", "workflow_id": instanceID, "status": "APPROVED"})
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

// CreateTaskHandler publishes a command to create a task to the TASKS stream
func (h *ERPHandler) CreateTaskHandler(c echo.Context) error {
	var cmd types.CreateTaskCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.Title == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Title is required"})
	}

	if cmd.ID == "" {
		cmd.ID = "task_" + uuid.New().String()[:8]
	}

	msg := h.newContextMsg("erp.tasks.task.cmd.create", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish task creation command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "CreateTask_Request", fmt.Sprintf("Queued creation of task '%s' (ID: %s)", cmd.Title, cmd.ID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Task creation queued"})
}

// UpdateTaskStatusHandler publishes a status update command to the TASKS stream
func (h *ERPHandler) UpdateTaskStatusHandler(c echo.Context) error {
	var cmd types.UpdateTaskStatusCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.TaskID == "" || cmd.Status == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "TaskID and Status are required"})
	}

	msg := h.newContextMsg("erp.tasks.task.cmd.update_status", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish status update command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "UpdateTaskStatus_Request", fmt.Sprintf("Queued status update of task %s to %s", cmd.TaskID, cmd.Status))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Status update queued"})
}

// UpdateInventoryThresholdHandler publishes a reorder threshold update to the INVENTORY stream
func (h *ERPHandler) UpdateInventoryThresholdHandler(c echo.Context) error {
	var cmd types.UpdateInventoryThresholdCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.ItemID == "" || cmd.ReorderThreshold < 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "ItemID and non-negative ReorderThreshold are required"})
	}

	msg := h.newContextMsg("erp.inventory.item.cmd.update_threshold", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish threshold update command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "UpdateThreshold_Request", fmt.Sprintf("Queued threshold update for %s to %d", cmd.ItemID, cmd.ReorderThreshold))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Threshold update queued"})
}

// workflowCommandSubjects is an allow-list: workflow records may only release
// known internal commands, never an arbitrary NATS subject supplied by a client.
var workflowCommandSubjects = map[string]bool{
	"erp.tasks.materials.cmd.fulfill": true,
	"erp.tasks.timesheet.cmd.approve": true,
	"erp.inventory.item.cmd.create":   true,
	"erp.finance.payment.cmd.record":  true,
}

func (h *ERPHandler) createWorkflow(c echo.Context, entityType, entityID, workflowKey, subject string, payload any) (string, error) {
	if !workflowCommandSubjects[subject] {
		return "", fmt.Errorf("unsupported workflow command subject")
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	err = h.db.CreateWorkflowRequest(c.Request().Context(), tenantID, id, entityType, entityID, workflowKey, userID, subject, raw)
	if err != nil {
		return "", err
	}
	h.sqliteDB.Log(tenantID, userID, "Workflow_Requested",
		fmt.Sprintf("%s %s created for %s/%s", workflowKey, id, entityType, entityID))
	return id, nil
}

// LedgerHandler returns immutable business transactions for finance users.
func (h *ERPHandler) LedgerHandler(c echo.Context) error {
	tenantID := c.Get(middleware.ContextTenantID).(string)
	entries, err := h.db.ListLedger(c.Request().Context(), tenantID, c.QueryParam("task_id"), 200)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load business ledger"})
	}
	return c.JSON(http.StatusOK, entries)
}

func (h *ERPHandler) ProfitabilityHandler(c echo.Context) error {
	tenantID := c.Get(middleware.ContextTenantID).(string)
	rows, err := h.db.GetProfitability(c.Request().Context(), tenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load profitability"})
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *ERPHandler) ReverseLedgerHandler(c echo.Context) error {
	var in struct {
		EntryID string `json:"entry_id"`
		Reason  string `json:"reason"`
	}
	if err := c.Bind(&in); err != nil || in.EntryID == "" || in.Reason == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "entry_id and reason are required"})
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	actor := c.Get(middleware.ContextUserID).(string)
	if err := h.db.ReverseLedgerEntry(c.Request().Context(), tenantID, in.EntryID, actor, in.Reason); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	h.sqliteDB.Log(tenantID, actor, "Ledger_Reversal", fmt.Sprintf("Reversed ledger entry %s: %s", in.EntryID, in.Reason))
	return c.JSON(http.StatusOK, map[string]string{"message": "ledger entry reversed"})
}

// CreateWorkflowInstanceHandler creates a formal maker-checker workflow instance.
func (h *ERPHandler) CreateWorkflowInstanceHandler(c echo.Context) error {
	var in struct {
		EntityType     string          `json:"entity_type"`
		EntityID       string          `json:"entity_id"`
		WorkflowKey    string          `json:"workflow_key"`
		CommandSubject string          `json:"command_subject"`
		CommandPayload json.RawMessage `json:"command_payload"`
	}
	if err := c.Bind(&in); err != nil || in.EntityType == "" || in.EntityID == "" || in.WorkflowKey == "" || in.CommandSubject == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "entity_type, entity_id, workflow_key and command_subject are required"})
	}
	if !workflowCommandSubjects[in.CommandSubject] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "command_subject is not an approved workflow command"})
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	payload := in.CommandPayload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	id := uuid.NewString()
	_, err := h.db.Pool.Exec(c.Request().Context(), `
		INSERT INTO workflow_instances
		(id,tenant_id,entity_type,entity_id,workflow_key,requested_by,command_subject,command_payload)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`,
		id, tenantID, in.EntityType, in.EntityID, in.WorkflowKey, userID, in.CommandSubject, string(payload))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create workflow instance"})
	}
	return c.JSON(http.StatusCreated, map[string]string{"id": id, "status": "REQUESTED"})
}

// WorkflowDecisionHandler advances or rejects a workflow while enforcing separation of duties.
func (h *ERPHandler) WorkflowDecisionHandler(c echo.Context) error {
	var d types.WorkflowDecision
	if err := c.Bind(&d); err != nil || d.InstanceID == "" || d.Action == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "instance_id and action are required"})
	}
	tenantID := c.Get(middleware.ContextTenantID).(string)
	actor := c.Get(middleware.ContextUserID).(string)

	var requested, status string
	err := h.db.Pool.QueryRow(c.Request().Context(),
		`SELECT requested_by,status FROM workflow_instances WHERE id=$1 AND tenant_id=$2`,
		d.InstanceID, tenantID).Scan(&requested, &status)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "workflow instance not found"})
	}
	if requested == actor && d.Action != "return" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "segregation of duties: requester cannot approve or execute own workflow"})
	}

	allowed := map[string]string{
		"approve": "APPROVED", "reject": "REJECTED", "return": "RETURNED",
		"execute": "EXECUTED", "reconcile": "RECONCILED",
	}
	next, ok := allowed[d.Action]
	if !ok {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported workflow action"})
	}
	valid := map[string][]string{
		"approve":   {"REQUESTED", "RETURNED"},
		"reject":    {"REQUESTED", "RETURNED", "APPROVED"},
		"return":    {"REQUESTED", "APPROVED", "EXECUTED"},
		"execute":   {"APPROVED"},
		"reconcile": {"EXECUTED"},
	}
	good := false
	for _, v := range valid[d.Action] {
		if v == status {
			good = true
			break
		}
	}
	if !good {
		return c.JSON(http.StatusConflict, map[string]string{"error": "invalid workflow transition"})
	}

	if d.Action == "execute" {
		headers := map[string]string{
			types.HeaderTenantID:   tenantID,
			types.HeaderUserID:     actor,
			types.HeaderUserRoles:  c.Get(middleware.ContextRoles).(string),
			types.HeaderUserRegion: c.Get(middleware.ContextRegion).(string),
			types.HeaderTraceID:    uuid.NewString(),
		}
		if _, err := h.db.ExecuteWorkflowTransaction(c.Request().Context(), tenantID, d.InstanceID, actor, d.Reason, headers); err != nil {
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
	} else {
		_, err = h.db.Pool.Exec(c.Request().Context(), `
			UPDATE workflow_instances
			SET status=$1,
				checked_by=CASE WHEN $2 IN ('approve','reject','return') THEN $3 ELSE checked_by END,
				reason=$4, updated_at=now()
			WHERE id=$5 AND tenant_id=$6`,
			next, d.Action, actor, d.Reason, d.InstanceID, tenantID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "workflow update failed"})
		}
		_, err = h.db.Pool.Exec(c.Request().Context(),
			`INSERT INTO workflow_decisions(id,instance_id,tenant_id,actor_id,action,reason) VALUES($1,$2,$3,$4,$5,$6)`,
			uuid.NewString(), d.InstanceID, tenantID, actor, d.Action, d.Reason)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "workflow audit write failed"})
		}
	}
	h.sqliteDB.Log(tenantID, actor, "Workflow_Decision", fmt.Sprintf("%s %s -> %s", d.InstanceID, d.Action, next))
	return c.JSON(http.StatusOK, map[string]string{"message": "workflow transitioned", "status": next})
}

// ListWorkflowInstancesHandler returns tenant-scoped workflow instances for governance UI.
func (h *ERPHandler) ListWorkflowInstancesHandler(c echo.Context) error {
	tenantID := c.Get(middleware.ContextTenantID).(string)
	rows, err := h.db.Pool.Query(c.Request().Context(), `SELECT id,entity_type,entity_id,workflow_key,step,status,requested_by,COALESCE(checked_by,''),COALESCE(executed_by,''),COALESCE(reconciled_by,''),COALESCE(reason,''),created_at,updated_at FROM workflow_instances WHERE tenant_id=$1 ORDER BY updated_at DESC LIMIT 100`, tenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load workflows"})
	}
	defer rows.Close()
	out := []types.WorkflowInstance{}
	for rows.Next() {
		var w types.WorkflowInstance
		if err := rows.Scan(&w.ID, &w.EntityType, &w.EntityID, &w.WorkflowKey, &w.Step, &w.Status, &w.RequestedBy, &w.CheckedBy, &w.ExecutedBy, &w.ReconciledBy, &w.Reason, &w.CreatedAt, &w.UpdatedAt); err == nil {
			out = append(out, w)
		}
	}
	return c.JSON(http.StatusOK, out)
}

// Customer/project lifecycle handlers keep CRM, commercial, delivery and CX in one chain.
func (h *ERPHandler) CreateLeadHandler(c echo.Context) error {
	var in struct {
		Name   string `json:"name"`
		Phone  string `json:"phone"`
		Email  string `json:"email"`
		Source string `json:"source"`
	}
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	if in.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
	}
	lead := types.Lead{ID: "lead_" + uuid.NewString()[:8], TenantID: c.Get(middleware.ContextTenantID).(string), Name: in.Name, Phone: in.Phone, Email: in.Email, Source: in.Source, OwnerID: c.Get(middleware.ContextUserID).(string), CreatedAt: time.Now()}
	if err := h.db.CreateLeadTransaction(c.Request().Context(), &lead); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, lead)
}

func (h *ERPHandler) ConvertLeadHandler(c echo.Context) error {
	var in struct {
		LeadID string `json:"lead_id"`
	}
	if err := c.Bind(&in); err != nil || in.LeadID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "lead_id is required"})
	}
	customer, err := h.db.ConvertLeadToCustomer(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.LeadID, c.Get(middleware.ContextUserID).(string))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, customer)
}

func (h *ERPHandler) CreateQuoteHandler(c echo.Context) error {
	var q types.CreateQuotePayload
	if err := c.Bind(&q); err != nil || q.CustomerID == "" || q.Title == "" || q.Amount <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id, title and positive amount are required"})
	}
	q.ID = "quote_" + uuid.NewString()[:8]
	quote := types.Quote{ID: q.ID, TenantID: c.Get(middleware.ContextTenantID).(string), CustomerID: q.CustomerID, LeadID: q.LeadID, Title: q.Title, Amount: q.Amount, ValidUntil: q.ValidUntil, CreatedBy: c.Get(middleware.ContextUserID).(string), CreatedAt: time.Now()}
	if err := h.db.CreateQuoteTransaction(c.Request().Context(), &quote); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, quote)
}

func (h *ERPHandler) SubmitQuoteApprovalHandler(c echo.Context) error {
	var in types.ApproveQuoteCommand
	if err := c.Bind(&in); err != nil || in.QuoteID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "quote_id is required"})
	}
	tenant := c.Get(middleware.ContextTenantID).(string)
	actor := c.Get(middleware.ContextUserID).(string)
	var createdBy string
	var status string
	if err := h.db.Pool.QueryRow(c.Request().Context(), `SELECT created_by,status FROM quotes WHERE id=$1 AND tenant_id=$2`, in.QuoteID, tenant).Scan(&createdBy, &status); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "quote not found"})
	}
	if createdBy == actor {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "quote creator cannot approve own quote"})
	}
	if status != "Draft" && status != "Returned" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "quote cannot be submitted from current status"})
	}
	_, err := h.db.Pool.Exec(c.Request().Context(), `UPDATE quotes SET status='Pending_Approval' WHERE id=$1 AND tenant_id=$2`, in.QuoteID, tenant)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to submit quote"})
	}
	return c.JSON(http.StatusAccepted, map[string]string{"message": "quote submitted for approval", "quote_id": in.QuoteID, "status": "Pending_Approval"})
}

func (h *ERPHandler) ApproveQuoteHandler(c echo.Context) error {
	var in types.ApproveQuoteCommand
	if err := c.Bind(&in); err != nil || in.QuoteID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "quote_id is required"})
	}
	if err := h.db.ApproveQuoteTransaction(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.QuoteID, c.Get(middleware.ContextUserID).(string)); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "quote approved", "quote_id": in.QuoteID, "status": "Approved"})
}

func (h *ERPHandler) CreateProjectHandler(c echo.Context) error {
	var p types.CreateProjectPayload
	if err := c.Bind(&p); err != nil || p.CustomerID == "" || p.Name == "" || p.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id, name and region are required"})
	}
	p.ID = "proj_" + uuid.NewString()[:8]
	project := types.Project{ID: p.ID, TenantID: c.Get(middleware.ContextTenantID).(string), CustomerID: p.CustomerID, QuoteID: p.QuoteID, Name: p.Name, Region: p.Region, StartDate: p.StartDate, TargetDate: p.TargetDate, CreatedBy: c.Get(middleware.ContextUserID).(string), CreatedAt: time.Now()}
	if err := h.db.CreateProjectTransaction(c.Request().Context(), &project); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, project)
}

func (h *ERPHandler) LinkProjectTaskHandler(c echo.Context) error {
	var in struct {
		ProjectID, TaskID, CustomerID string `json:"-"`
	}
	var raw map[string]string
	if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
	}
	in.ProjectID = raw["project_id"]
	in.TaskID = raw["task_id"]
	in.CustomerID = raw["customer_id"]
	if in.ProjectID == "" || in.TaskID == "" || in.CustomerID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id, task_id and customer_id are required"})
	}
	if err := h.db.LinkTaskToProject(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ProjectID, in.TaskID, in.CustomerID); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "task linked to project"})
}

func (h *ERPHandler) AddProjectEvidenceHandler(c echo.Context) error {
	var e types.CreateEvidencePayload
	if err := c.Bind(&e); err != nil || e.ProjectID == "" || e.EvidenceType == "" || e.URL == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id, evidence_type and url are required"})
	}
	x := types.ProjectEvidence{ID: "evidence_" + uuid.NewString()[:8], TenantID: c.Get(middleware.ContextTenantID).(string), ProjectID: e.ProjectID, TaskID: e.TaskID, EvidenceType: e.EvidenceType, URL: e.URL, Note: e.Note, CapturedBy: c.Get(middleware.ContextUserID).(string), CreatedAt: time.Now()}
	if err := h.db.AddProjectEvidence(c.Request().Context(), &x); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, x)
}

func (h *ERPHandler) AddCustomerSatisfactionHandler(c echo.Context) error {
	var s types.CreateSatisfactionPayload
	if err := c.Bind(&s); err != nil || s.CustomerID == "" || s.Rating < 1 || s.Rating > 5 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "customer_id and rating 1-5 are required"})
	}
	x := types.CustomerSatisfaction{ID: "csat_" + uuid.NewString()[:8], TenantID: c.Get(middleware.ContextTenantID).(string), CustomerID: s.CustomerID, ProjectID: s.ProjectID, Rating: s.Rating, Comment: s.Comment, CreatedAt: time.Now()}
	if err := h.db.AddCustomerSatisfaction(c.Request().Context(), &x); err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, x)
}

// v11 project execution control: assignment, scheduling, BOM and completion review.
func (h *ERPHandler) CreateProjectAssignmentHandler(c echo.Context) error {
	var in struct {
		ProjectID    string `json:"project_id"`
		TaskID       string `json:"task_id"`
		TechnicianID string `json:"technician_id"`
		Role         string `json:"role"`
	}
	if err := c.Bind(&in); err != nil || in.ProjectID == "" || in.TechnicianID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id and technician_id are required"})
	}
	if in.Role == "" {
		in.Role = "Technician"
	}
	err := h.db.CreateProjectAssignment(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ProjectID, in.TaskID, in.TechnicianID, in.Role, c.Get(middleware.ContextUserID).(string))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "Assigned", "project_id": in.ProjectID, "technician_id": in.TechnicianID})
}
func (h *ERPHandler) CreateProjectScheduleHandler(c echo.Context) error {
	var in struct {
		ProjectID      string     `json:"project_id"`
		TaskID         string     `json:"task_id"`
		ScheduledStart time.Time  `json:"scheduled_start"`
		ScheduledEnd   *time.Time `json:"scheduled_end"`
		Notes          string
	}
	if err := c.Bind(&in); err != nil || in.ProjectID == "" || in.ScheduledStart.IsZero() {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id and scheduled_start are required"})
	}
	err := h.db.CreateProjectSchedule(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ProjectID, in.TaskID, in.ScheduledStart, in.ScheduledEnd, in.Notes, c.Get(middleware.ContextUserID).(string))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "Scheduled"})
}
func (h *ERPHandler) CreateProjectBOMHandler(c echo.Context) error {
	var in struct {
		ProjectID string  `json:"project_id"`
		TaskID    string  `json:"task_id"`
		ItemName  string  `json:"item_name"`
		Quantity  float64 `json:"quantity"`
		UnitCost  float64 `json:"unit_cost"`
	}
	if err := c.Bind(&in); err != nil || in.ProjectID == "" || in.ItemName == "" || in.Quantity <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id, item_name and positive quantity are required"})
	}
	err := h.db.CreateProjectBOM(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ProjectID, in.TaskID, in.ItemName, in.Quantity, in.UnitCost, c.Get(middleware.ContextUserID).(string))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "Requested"})
}
func (h *ERPHandler) SubmitCompletionReviewHandler(c echo.Context) error {
	var in struct {
		ProjectID string `json:"project_id"`
		TaskID    string `json:"task_id"`
	}
	if err := c.Bind(&in); err != nil || in.ProjectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id is required"})
	}
	err := h.db.SubmitCompletionReview(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ProjectID, in.TaskID, c.Get(middleware.ContextUserID).(string))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "Submitted"})
}
func (h *ERPHandler) ReviewCompletionHandler(c echo.Context) error {
	var in struct {
		ReviewID string `json:"review_id"`
		Status   string `json:"status"`
		Note     string `json:"note"`
	}
	if err := c.Bind(&in); err != nil || in.ReviewID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "review_id is required"})
	}
	err := h.db.ReviewCompletion(c.Request().Context(), c.Get(middleware.ContextTenantID).(string), in.ReviewID, c.Get(middleware.ContextUserID).(string), in.Status, in.Note)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": in.Status})
}
