package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"

	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

// OrderHandler publishes order events to NATS JetStream
type OrderHandler struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

// NewOrderHandler constructs a new OrderHandler
func NewOrderHandler(nc *nats.Conn, js nats.JetStreamContext) *OrderHandler {
	return &OrderHandler{nc: nc, js: js}
}

// CreateOrderHandler processes order creation command and publishes to SALES stream (erp.sales.order.v1.created)
func (h *OrderHandler) CreateOrderHandler(c echo.Context) error {
	userID := c.Get(middleware.ContextUserID).(string)
	roles := c.Get(middleware.ContextRoles).(string)
	region := c.Get(middleware.ContextRegion).(string)

	var cmd types.OrderCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid payload format"})
	}

	if cmd.OrderID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "OrderID is required"})
	}

	// Create CloudEvent payload
	cloudEvent := types.CloudEvent[types.OrderCommand]{
		SpecVersion:     "1.0",
		ID:              uuid.New().String(),
		Source:          "/erp/sales/checkout",
		Type:            "erp.sales.order.v1.created",
		Time:            time.Now().UTC(),
		DataContentType: "application/json",
		Data:            cmd,
	}

	payload, err := json.Marshal(cloudEvent)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to marshal payload"})
	}

	// Construct NATS message with context propagation headers
	msg := nats.NewMsg("erp.sales.order.v1.created")
	msg.Header.Set(types.HeaderUserID, userID)
	msg.Header.Set(types.HeaderUserRoles, roles)
	msg.Header.Set(types.HeaderUserRegion, region)
	msg.Header.Set(types.HeaderTraceID, uuid.New().String())
	msg.Data = payload

	// Publish with deduplication header
	msg.Header.Set("Nats-Msg-Id", cloudEvent.ID)

	_, err = h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to publish event to the bus"})
	}

	return c.JSON(http.StatusAccepted, map[string]string{
		"message":  "Order creation event published",
		"event_id": cloudEvent.ID,
	})
}

// ApproveOrderHandler processes order approval command and publishes to erp.sales.order.cmd.approve
func (h *OrderHandler) ApproveOrderHandler(c echo.Context) error {
	userID := c.Get(middleware.ContextUserID).(string)
	roles := c.Get(middleware.ContextRoles).(string)
	region := c.Get(middleware.ContextRegion).(string)

	var cmd types.OrderCommand
	if err := c.Bind(&cmd); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid payload format"})
	}

	if cmd.OrderID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "OrderID is required"})
	}

	// Construct NATS command message with headers
	msg := nats.NewMsg("erp.sales.order.cmd.approve")
	msg.Header.Set(types.HeaderUserID, userID)
	msg.Header.Set(types.HeaderUserRoles, roles)
	msg.Header.Set(types.HeaderUserRegion, region)
	msg.Header.Set(types.HeaderTraceID, uuid.New().String())

	payload, err := json.Marshal(cmd)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to marshal payload"})
	}
	msg.Data = payload

	_, err = h.js.PublishMsg(msg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to queue command"})
	}

	return c.JSON(http.StatusAccepted, map[string]string{
		"message": "Order approval command queued",
	})
}
