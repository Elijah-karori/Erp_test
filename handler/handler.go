package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"

	"erp-event-bus/db"
	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

// ERPHandler publishes tenant, inventory, finance and task messages to JetStream
type ERPHandler struct {
	nc       *nats.Conn
	js       nats.JetStreamContext
	sqliteDB *db.SQLiteDB
}

// NewERPHandler constructs a new ERPHandler with SQLite logging
func NewERPHandler(nc *nats.Conn, js nats.JetStreamContext, sdb *db.SQLiteDB) *ERPHandler {
	return &ERPHandler{nc: nc, js: js, sqliteDB: sdb}
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
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid body"})
	}

	if cmd.TimesheetID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "TimesheetID is required"})
	}

	msg := h.newContextMsg("erp.tasks.timesheet.cmd.approve", c)
	payload, _ := json.Marshal(cmd)
	msg.Data = payload

	_, err := h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish timesheet approval command"})
	}

	tenantID := c.Get(middleware.ContextTenantID).(string)
	userID := c.Get(middleware.ContextUserID).(string)
	h.sqliteDB.Log(tenantID, userID, "ApproveTimesheet_Request", fmt.Sprintf("Queued approval of timesheet %s", cmd.TimesheetID))

	return c.JSON(http.StatusAccepted, map[string]string{"message": "Timesheet approval queued"})
}
