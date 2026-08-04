package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"erp-event-bus/consumer"
	"erp-event-bus/db"
	"erp-event-bus/handler"
	"erp-event-bus/internal/eventbus"
	"erp-event-bus/middleware"
	"erp-event-bus/types"
)

func cleanAndSeedDB(t *testing.T) *db.Database {
	dbURL := "postgres://erp_app:postgres@localhost:5432/erp_db?sslmode=disable"
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("Failed to connect to test DB: %v", err)
	}

	ctx := context.Background()
	tables := []string{
		"timesheets", "tasks", "payments", "invoices", "procurement_orders",
		"material_requests", "inventory_history", "inventory_items", "users", "roles", "tenants", "erp_logs",
	}
	for _, table := range tables {
		_, _ = pool.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
	}

	database := db.NewDatabase(pool)
	return database
}

func TestHierarchicalRoleClearance_Middleware(t *testing.T) {
	e := echo.New()
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

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
	assert.True(t, database.CheckPermission("tenant_safari", "manager", "tasks:approve"))
	assert.True(t, database.CheckPermission("tenant_safari", "manager", "inventory:read"))

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
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	// Alice (tenant_admin) is manager's parent. Bob (manager) is Charlie's (field_technician) parent.
	assert.True(t, database.IsSubordinate("tenant_safari", "manager", "field_technician"))
	assert.True(t, database.IsSubordinate("tenant_safari", "tenant_admin", "manager"))
	assert.False(t, database.IsSubordinate("tenant_safari", "field_technician", "manager")) // Inverse is false
}

func TestMultiTenantIsolationFlow(t *testing.T) {
	bus, err := eventbus.Start()
	assert.NoError(t, err)
	defer func() {
		_ = bus.Conn.Drain()
		bus.Server.Shutdown()
	}()

	nc := bus.Conn
	jsContext := bus.JS

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
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	sdb, err := db.InitSQLite(":memory:")
	assert.NoError(t, err)
	sdb.Pool = database.Pool

	e := echo.New()
	reqBody := `{"invoice_id": "inv_safari_1", "amount": 1000.0, "payment_method": "Mpesa_Paybill", "reference": "MPESA-TEST-99"}`
	req := httptest.NewRequest(http.MethodPost, "/api/finance/payments", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization-Tenant-Id", "tenant_safari")
	req.Header.Set("Authorization-User-Id", "usr_safari_mgr")
	req.Header.Set("Authorization-Roles", "manager")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	h := handler.NewERPHandler(nc, jsContext, sdb, database)

	auth := middleware.MockAuthMiddleware()
	handlerFunc := auth(h.RecordPaymentHandler)

	err = handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestInventoryReorderProcurementAlert(t *testing.T) {
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	mr, proc, err := database.ApproveMaterialRequestTransaction("req_safari_1", "tenant_safari", "usr_safari_admin")
	assert.NoError(t, err)
	assert.NotNil(t, mr)
	_ = proc
	assert.Equal(t, "Fulfilled", mr.Status)
	assert.Equal(t, "SN-HUA-9901", mr.AllocatedSN)

	// Let's verify that a procurement_orders row is auto-created because of reorder threshold!
	ctx := context.Background()
	var count int
	err = database.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM procurement_orders WHERE tenant_id = 'tenant_safari' AND item_name = 'Huawei GPON ONU'").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count, "Auto-created procurement order should exist under tenant_safari for low stock warning")

	// If we approve a second request, it will procurement fallback as usual
	// Let's verify that too
	_, _ = database.Pool.Exec(ctx, "INSERT INTO material_requests (id, tenant_id, requester_id, item_name, status, created_at) VALUES ('req_safari_2', 'tenant_safari', 'usr_safari_tech', 'Huawei GPON ONU', 'Pending_Leader_Approval', NOW())")

	mr2, proc2, err := database.ApproveMaterialRequestTransaction("req_safari_2", "tenant_safari", "usr_safari_admin")
	assert.NoError(t, err)
	assert.NotNil(t, mr2)
	assert.Equal(t, "Procuring", mr2.Status) // Fallback as there is no stock left
	assert.NotNil(t, proc2)
	assert.Equal(t, "Bidding", proc2.Status)
}

func TestTaskDependencyEnforcement(t *testing.T) {
	bus, err := eventbus.Start()
	assert.NoError(t, err)
	defer func() {
		_ = bus.Conn.Drain()
		bus.Server.Shutdown()
	}()

	nc := bus.Conn
	jsContext := bus.JS

	// Configure stream and consumer
	_, err = jsContext.AddStream(&nats.StreamConfig{
		Name:     "TASKS",
		Subjects: []string{"erp.tasks.>"},
		Storage:  nats.MemoryStorage,
	})
	assert.NoError(t, err)
	defer jsContext.DeleteStream("TASKS")

	_, err = jsContext.AddConsumer("TASKS", &nats.ConsumerConfig{
		Durable:       "TaskTimesheetWorker",
		FilterSubject: "erp.tasks.task.cmd.>",
	})
	assert.NoError(t, err)

	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	sdb, err := db.InitSQLite(":memory:")
	assert.NoError(t, err)
	sdb.Pool = database.Pool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = consumer.StartERPProcessors(ctx, nc, database, sdb, nil)
	assert.NoError(t, err)

	// Create Task A (Pending)
	taskA := &types.Task{
		ID:         "task_A",
		TenantID:   "tenant_safari",
		Title:      "VPC Setup",
		AssignedTo: "usr_safari_tech",
		CreatedBy:  "usr_safari_mgr",
		Status:     "Pending",
		Region:     "Nairobi",
	}
	database.SaveTask(taskA)

	// Create Task B (Depends on Task A, Pending)
	taskB := &types.Task{
		ID:         "task_B",
		TenantID:   "tenant_safari",
		Title:      "Deploy App",
		AssignedTo: "usr_safari_tech",
		CreatedBy:  "usr_safari_mgr",
		Status:     "Pending",
		Region:     "Nairobi",
		DependsOn:  "task_A",
	}
	database.SaveTask(taskB)

	// 1. Try to set Task B to In_Progress. This should fail because Task A is Pending.
	cmd := types.UpdateTaskStatusCommand{
		TaskID: "task_B",
		Status: "In_Progress",
	}
	payload, _ := json.Marshal(cmd)
	msg := nats.NewMsg("erp.tasks.task.cmd.update_status")
	msg.Header.Set(types.HeaderTenantID, "tenant_safari")
	msg.Header.Set(types.HeaderUserID, "usr_safari_mgr")
	msg.Header.Set(types.HeaderUserRoles, "manager")
	msg.Data = payload

	_, err = jsContext.PublishMsg(msg)
	assert.NoError(t, err)

	time.Sleep(150 * time.Millisecond)

	// Task B status should still be Pending!
	taskBChecked, err := database.GetTask("task_B")
	assert.NoError(t, err)
	assert.Equal(t, "Pending", taskBChecked.Status)

	// 2. Set Task A to Completed
	taskA.Status = "Completed"
	database.SaveTask(taskA)

	// 3. Try to set Task B to In_Progress again. This should now succeed!
	_, err = jsContext.PublishMsg(msg)
	assert.NoError(t, err)

	time.Sleep(150 * time.Millisecond)

	taskBChecked, err = database.GetTask("task_B")
	assert.NoError(t, err)
	assert.Equal(t, "In_Progress", taskBChecked.Status)
}

func TestRegionalTimesheetApproval(t *testing.T) {
	e := echo.New()
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	sdb, err := db.InitSQLite(":memory:")
	assert.NoError(t, err)
	sdb.Pool = database.Pool

	h := handler.NewERPHandler(nil, nil, sdb, database)

	// Create a task in Mombasa
	task := &types.Task{
		ID:         "task_mombasa",
		TenantID:   "tenant_safari",
		Title:      "Mombasa FTTH",
		AssignedTo: "usr_safari_tech",
		CreatedBy:  "usr_safari_mgr",
		Status:     "Pending",
		Region:     "Mombasa",
	}
	database.SaveTask(task)

	// Create a timesheet for it
	ts := &types.Timesheet{
		ID:       "ts_mombasa",
		TenantID: "tenant_safari",
		TaskID:   "task_mombasa",
		UserID:   "usr_safari_tech",
		Hours:    5.0,
		Status:   "Submitted",
	}
	database.SaveTimesheet(ts)

	// Bob Manager is in Nairobi, so trying to approve Mombasa timesheet should 403 (StatusForbidden)
	req := httptest.NewRequest(http.MethodPost, "/tasks/timesheets/approve", strings.NewReader(`{"timesheet_id": "ts_mombasa"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization-Tenant-Id", "tenant_safari")
	req.Header.Set("Authorization-User-Id", "usr_safari_mgr")
	req.Header.Set("Authorization-Roles", "manager")
	req.Header.Set("Authorization-Region", "Nairobi")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	auth := middleware.MockAuthMiddleware()
	handlerFunc := auth(h.ApproveTimesheetHandler)

	err = handlerFunc(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestInvitationAndHierarchy(t *testing.T) {
	database := cleanAndSeedDB(t)
	defer database.Pool.Close()

	// 1. Create invitation
	inv := &types.Invitation{
		ID:        "inv_test_jordan",
		TenantID:  "tenant_safari",
		Email:     "jordan@safari.test",
		Name:      "Jordan Smith",
		RoleName:  "field_technician",
		Region:    "Nairobi",
		ManagerID: "usr_safari_mgr",
		Token:     "test-token-xyz-123",
		Status:    "Pending",
		CreatedAt: time.Now(),
	}

	err := database.CreateInvitation(inv)
	assert.NoError(t, err)

	// 2. Retrieve invitation by token
	retrieved, err := database.GetInvitationByToken("test-token-xyz-123")
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, "Jordan Smith", retrieved.Name)
	assert.Equal(t, "field_technician", retrieved.RoleName)
	assert.Equal(t, "usr_safari_mgr", retrieved.ManagerID)

	// 3. Accept invitation
	user, err := database.AcceptInvitation("test-token-xyz-123", "hashedpassword123")
	assert.NoError(t, err)
	assert.NotNil(t, user)
	assert.Equal(t, "Jordan Smith", user.Name)
	assert.Equal(t, "jordan@safari.test", user.Email)
	assert.Equal(t, "usr_safari_mgr", user.ManagerID)

	// Verify user is in db
	dbUser, err := database.GetUser(user.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Jordan Smith", dbUser.Name)
	assert.Equal(t, "usr_safari_mgr", dbUser.ManagerID)

	// 4. Update manager
	err = database.UpdateUserManager(user.ID, "usr_safari_admin")
	assert.NoError(t, err)

	dbUser, _ = database.GetUser(user.ID)
	assert.Equal(t, "usr_safari_admin", dbUser.ManagerID)

	// 5. Update role
	err = database.UpdateUserRole(user.ID, "manager")
	assert.NoError(t, err)

	dbUser, _ = database.GetUser(user.ID)
	assert.Equal(t, "manager", dbUser.RoleName)
}
