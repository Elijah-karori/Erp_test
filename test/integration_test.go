package test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"erp-event-bus/db"
	"erp-event-bus/handler"
	"erp-event-bus/middleware"
)

func TestHierarchicalRoleClearance_Middleware(t *testing.T) {
	e := echo.New()
	database := db.NewDatabase()

	// 1. Success case: Charlie (field_technician) can perform reading tasks / inventory (matches "inventory:read")
	req := httptest.NewRequest(http.MethodPost, "/inventory", nil)
	req.Header.Set("Authorization-Tenant-Id", "tenant_safari")
	req.Header.Set("Authorization-User-Id", "usr_safari_tech")
	req.Header.Set("Authorization-Roles", "field_technician")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	auth := middleware.MockAuthMiddleware()
	clearance := middleware.ModuleClearanceMiddleware(database, "inventory:read")

	handlerFunc := auth(clearance(func(ctx echo.Context) error {
		return ctx.String(http.StatusOK, "cleared")
	}))

	err := handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// 2. Parent inheritance check: Bob Manager has "manager" role.
	// Because "manager" inherits from "tenant_admin", checking check permission
	assert.True(t, database.CheckPermission("manager", "tasks:approve"))
	assert.True(t, database.CheckPermission("manager", "inventory:read"))

	// 3. Reject case: field_technician cannot update finance (matches "finance:write")
	reqFail := httptest.NewRequest(http.MethodPost, "/finance/payments", nil)
	reqFail.Header.Set("Authorization-Tenant-Id", "tenant_safari")
	reqFail.Header.Set("Authorization-User-Id", "usr_safari_tech")
	reqFail.Header.Set("Authorization-Roles", "field_technician")
	recFail := httptest.NewRecorder()

	cFail := e.NewContext(reqFail, recFail)
	clearanceFail := middleware.ModuleClearanceMiddleware(database, "finance:write")
	handlerFuncFail := auth(clearanceFail(func(ctx echo.Context) error {
		return ctx.String(http.StatusOK, "cleared")
	}))

	err = handlerFuncFail(cFail)
	assert.Error(t, err)
	he, ok := err.(*echo.HTTPError)
	assert.True(t, ok)
	assert.Equal(t, http.StatusForbidden, he.Code)
}

func TestHierarchy_SubordinateTimesheetsApproval(t *testing.T) {
	database := db.NewDatabase()

	// Alice (tenant_admin) is manager's parent. Bob (manager) is Charlie's (field_technician) parent.
	assert.True(t, database.IsSubordinate("manager", "field_technician"))
	assert.True(t, database.IsSubordinate("tenant_admin", "manager"))
	assert.False(t, database.IsSubordinate("field_technician", "manager")) // Inverse is false
}

func TestMultiTenantIsolationFlow(t *testing.T) {
	// Fallback/Connection integration verification with NATS
	nc, err := nats.Connect(nats.DefaultURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skip("NATS Server not running locally on default port 4222, skipping NATS client-based flow.")
		return
	}
	defer nc.Close()

	jsContext, err := nc.JetStream()
	assert.NoError(t, err)

	// Configure stream and consumer
	_, err = jsContext.AddStream(&nats.StreamConfig{
		Name:     "FINANCE",
		Subjects: []string{"erp.finance.>"},
		Storage:  nats.MemoryStorage,
	})
	assert.NoError(t, err)
	defer jsContext.DeleteStream("FINANCE")

	_, err = jsContext.AddConsumer("FINANCE", &nats.ConsumerConfig{
		Durable:       "FinanceWorker",
		FilterSubject: "erp.finance.payment.cmd.>",
	})
	assert.NoError(t, err)

	// Build database with invoice ORD
	_ = db.NewDatabase()
	sdb, err := db.InitSQLite(":memory:")
	assert.NoError(t, err)

	e := echo.New()
	reqBody := `{"invoice_id": "inv_safari_1", "amount": 1000.0, "payment_method": "Mpesa_Paybill", "reference": "MPESA-TEST-99"}`
	req := httptest.NewRequest(http.MethodPost, "/api/finance/payments", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization-Tenant-Id", "tenant_safari")
	req.Header.Set("Authorization-User-Id", "usr_safari_mgr")
	req.Header.Set("Authorization-Roles", "manager")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	h := handler.NewERPHandler(nc, jsContext, sdb)

	auth := middleware.MockAuthMiddleware()
	handlerFunc := auth(h.RecordPaymentHandler)

	err = handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, rec.Code)
}
