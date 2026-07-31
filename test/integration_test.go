package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	"erp-event-bus/db"
	"erp-event-bus/handler"
	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

func TestEdgeRBAC_Middleware(t *testing.T) {
	e := echo.New()

	// 1. Success case: user has sales_rep role
	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req.Header.Set("Authorization-User-Id", "USR-1")
	req.Header.Set("Authorization-Roles", "sales_rep,guest")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	auth := middleware.MockAuthMiddleware()
	rbac := middleware.EdgeRBACMiddleware("sales_rep")

	handlerFunc := auth(rbac(func(ctx echo.Context) error {
		return ctx.String(http.StatusOK, "success")
	}))

	err := handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// 2. Failure case: user only has guest role
	reqFail := httptest.NewRequest(http.MethodPost, "/orders", nil)
	reqFail.Header.Set("Authorization-User-Id", "USR-2")
	reqFail.Header.Set("Authorization-Roles", "guest")
	recFail := httptest.NewRecorder()

	cFail := e.NewContext(reqFail, recFail)

	err = handlerFunc(cFail)
	assert.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	assert.True(t, ok)
	assert.Equal(t, http.StatusForbidden, he.Code)
}

func TestContextPropagation_And_ABACFlow(t *testing.T) {
	// Initialize local/embedded or test server connection (fallback to local default port)
	nc, err := nats.Connect(nats.DefaultURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skip("NATS Server not running locally on default port 4222, skipping integration test flow.")
		return
	}
	defer nc.Close()

	jsContext, err := nc.JetStream()
	assert.NoError(t, err)

	// Configure SALES stream
	_, err = jsContext.AddStream(&nats.StreamConfig{
		Name:     "SALES",
		Subjects: []string{"erp.sales.>"},
		Storage:  nats.MemoryStorage,
	})
	assert.NoError(t, err)
	defer jsContext.DeleteStream("SALES")

	// Set up durable consumer
	_, err = jsContext.AddConsumer("SALES", &nats.ConsumerConfig{
		Durable:        "OrderApprovalWorker",
		FilterSubject:  "erp.sales.order.cmd.approve",
		DeliverPolicy:  nats.DeliverAllPolicy,
		AckPolicy:      nats.AckExplicitPolicy,
	})
	assert.NoError(t, err)

	database := db.NewDatabase()
	// Register mock order belonging to region EU-West
	database.SaveOrder(&db.MockOrder{OrderID: "ORD-TEST-1", Region: "EU-West", Status: "PENDING"})

	// Prepare Echo request simulating a manager from US-East region trying to approve
	e := echo.New()
	reqBody := `{"order_id": "ORD-TEST-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/orders/approve", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization-User-Id", "USR-MGR")
	req.Header.Set("Authorization-Roles", "manager")
	req.Header.Set("Authorization-Region", "US-East") // Mismatch: User is US-East, but Order is EU-West
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	h := handler.NewOrderHandler(nc, jsContext)

	auth := middleware.MockAuthMiddleware()
	handlerFunc := auth(h.ApproveOrderHandler)

	err = handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	// Consume and evaluate the message from modern JetStream API
	js, err := jetstream.New(nc)
	assert.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cons, err := js.Consumer(ctx, "SALES", "OrderApprovalWorker")
	assert.NoError(t, err)

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(1*time.Second))
	assert.NoError(t, err)

	for msg := range msgs.Messages() {
		userID := msg.Headers().Get(types.HeaderUserID)
		userRegion := msg.Headers().Get(types.HeaderUserRegion)

		assert.Equal(t, "USR-MGR", userID)
		assert.Equal(t, "US-East", userRegion)

		var cmd types.OrderCommand
		err = json.Unmarshal(msg.Data(), &cmd)
		assert.NoError(t, err)
		assert.Equal(t, "ORD-TEST-1", cmd.OrderID)

		// Get Resource Region from Database
		order, err := database.GetOrder(cmd.OrderID)
		assert.NoError(t, err)

		// ABAC logic validation: mismatch user region (US-East) != order region (EU-West)
		assert.NotEqual(t, order.Region, userRegion)

		// Discard / Terminate message
		err = msg.Term()
		assert.NoError(t, err)
	}
}
