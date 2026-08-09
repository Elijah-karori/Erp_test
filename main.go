package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	echo_middleware "github.com/labstack/echo/v4/middleware"
	"github.com/nats-io/nats.go"

	"erp-event-bus/consumer"
	"erp-event-bus/db"
	"erp-event-bus/email"
	"erp-event-bus/handler"
	"erp-event-bus/internal/env"
	"erp-event-bus/internal/eventbus"
	"erp-event-bus/internal/outbox"
	"erp-event-bus/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// 1. Perform strict startup environment validation
	envConfig := env.ValidateAndLoad()

	emailSvc := email.NewEmailService()
	bus, err := eventbus.Start()
	if err != nil {
		log.Fatalf("failed to start embedded event bus: %v", err)
	}
	defer func() {
		_ = bus.Conn.Drain()
		bus.Server.Shutdown()
	}()

	nc := bus.Conn
	js := bus.JS

	config, err := pgxpool.ParseConfig(envConfig.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to parse DATABASE_URL: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Failed to create connection pool: %v", err)
	}
	defer pool.Close()

	// Setup Multi-Tenant Streams with multiple subjects support
	setupStream(js, "INVENTORY", []string{"erp.inventory.>"})
	setupStream(js, "FINANCE", []string{"erp.finance.>", "erp.customers.>"})
	setupStream(js, "TASKS", []string{"erp.tasks.>", "erp.users.>"})

	// Setup Durable Consumers with wildcards matching the expanded subjects
	setupConsumer(js, "INVENTORY", "InventoryWorker", "erp.inventory.>")
	setupConsumer(js, "FINANCE", "FinanceWorker", "erp.>")     // Matches all finance and customer events
	setupConsumer(js, "TASKS", "TaskTimesheetWorker", "erp.>") // Matches all tasks and users events

	// 4. Initialize SQLite Persistent Log Database
	sqliteDB, err := db.InitSQLite("erp.db")
	if err != nil {
		log.Fatalf("Failed to initialize SQLite persistent database: %v", err)
	}

	// Initialize In-Memory Database
	database := db.NewDatabase(pool)
	sqliteDB.Pool = pool

	// 5. Initialize durable outbox publisher and downstream JetStream consumers.
	// Workflow execution commits the outbox row in the same PostgreSQL transaction;
	// this publisher is responsible only for delivery and retry.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outboxPublisher := outbox.NewPublisher(pool, js)
	go outboxPublisher.Run(ctx)

	_ = consumer.StartERPProcessors(ctx, nc, database, sqliteDB, emailSvc)

	// 6. Initialize Echo Server
	e := echo.New()
	e.Use(echo_middleware.Logger())
	e.Use(echo_middleware.Recover())

	// UI Dashboard Endpoint (No Auth header required for page load itself)
	uiHandler := handler.NewUIHandler(database, sqliteDB, emailSvc)
	e.GET("/", uiHandler.ServeDashboard)
	e.GET("/activate.html", uiHandler.ServeActivationPage)

	// Public auth endpoints — no token required to reach these, since this
	// is where a token comes from in the first place.
	authHandler := handler.NewAuthHandler(database, sqliteDB, emailSvc)
	e.POST("/api/register", authHandler.RegisterHandler)
	e.POST("/api/login", authHandler.LoginHandler)
	e.GET("/api/invite/preview", uiHandler.PreviewInvitation)
	e.POST("/api/activate", uiHandler.ActivateUser)

	// Every other /api route requires a verified JWT from here on.
	api := e.Group("/api")
	api.Use(middleware.JWTAuthMiddleware())

	api.GET("/me", authHandler.MeHandler)

	// State and live log endpoints for UI rendering
	api.GET("/state", uiHandler.GetState)
	api.GET("/lifecycle", uiHandler.GetCustomerLifecycle, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.GET("/execution", uiHandler.GetProjectExecution, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.GET("/intelligence", uiHandler.GetFieldServiceIntelligence, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.GET("/field/telemetry", uiHandler.GetFieldTelemetry, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.POST("/field/attendance", uiHandler.RecordFieldAttendance, middleware.ModuleClearanceMiddleware(database, "timesheets:submit"))
	api.POST("/field/job-event", uiHandler.RecordFieldJobEvent, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.GET("/logs", uiHandler.GetLogs)
	api.POST("/rbac/update", uiHandler.UpdateRBAC, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/tenants", uiHandler.CreateTenant, middleware.ModuleClearanceMiddleware(database, "users:*"))
	// Previously ungated — any caller could create a user with any role,
	// including tenant_admin. Now requires an authenticated admin.
	api.POST("/users", uiHandler.CreateUser, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/users/invite", uiHandler.InviteUser, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/users/update-manager", uiHandler.UpdateUserManager, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/users/update-role", uiHandler.UpdateUserRole, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/tickets", uiHandler.CreateSupportTicket, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/tickets/convert", uiHandler.ConvertTicketToTask, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/inventory/add", uiHandler.AddManualInventoryItem, middleware.ModuleClearanceMiddleware(database, "inventory:write"))
	api.POST("/materials/invoice-note/update", uiHandler.UpdateInvoiceNote, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/procurement/confirm", uiHandler.ConfirmProcurementReceipt, middleware.ModuleClearanceMiddleware(database, "inventory:write"))
	// Previously registered directly on `e`, bypassing JWTAuthMiddleware
	// entirely — anyone could download the full audit log with no token.
	api.GET("/exports/excel", uiHandler.ExportLogsExcel)

	h := handler.NewERPHandler(nc, js, sqliteDB, database)

	// Inventory Endpoints: (Hierarchy Checks via middleware permission levels)
	api.POST("/inventory", h.CreateInventoryItemHandler, middleware.ModuleClearanceMiddleware(database, "inventory:write"))
	api.POST("/inventory/assign", h.AssignDeviceHandler, middleware.ModuleClearanceMiddleware(database, "inventory:*"))
	api.POST("/inventory/threshold", h.UpdateInventoryThresholdHandler, middleware.ModuleClearanceMiddleware(database, "inventory:*"))

	// Finance Endpoints:
	api.POST("/finance/payments", h.RecordPaymentHandler, middleware.ModuleClearanceMiddleware(database, "finance:write"))

	// Tasks Endpoints:
	api.POST("/tasks", h.CreateTaskHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.GET("/workflows", h.ListWorkflowInstancesHandler, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.GET("/ledger", h.LedgerHandler, middleware.ModuleClearanceMiddleware(database, "finance:read"))
	api.GET("/profitability", h.ProfitabilityHandler, middleware.ModuleClearanceMiddleware(database, "finance:read"))
	api.POST("/ledger/reverse", h.ReverseLedgerHandler, middleware.ModuleClearanceMiddleware(database, "finance:write"))
	api.POST("/workflows", h.CreateWorkflowInstanceHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/workflows/decision", h.WorkflowDecisionHandler, middleware.AnyModuleClearanceMiddleware(database, "tasks:approve", "timesheets:approve", "inventory:write", "finance:write"))
	api.POST("/tasks/status", h.UpdateTaskStatusHandler, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.POST("/tasks/timesheets/approve", h.ApproveTimesheetHandler, middleware.ModuleClearanceMiddleware(database, "timesheets:approve"))
	api.POST("/tasks/materials/request", h.SubmitMaterialRequestHandler, middleware.ModuleClearanceMiddleware(database, "tasks:read"))
	api.POST("/tasks/materials/approve", h.ApproveMaterialHandler, middleware.ModuleClearanceMiddleware(database, "tasks:approve"))
	api.POST("/customers", h.CreateCustomerHandler, middleware.ModuleClearanceMiddleware(database, "users:*"))
	api.POST("/lifecycle/leads", h.CreateLeadHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.POST("/lifecycle/leads/convert", h.ConvertLeadHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.POST("/lifecycle/quotes", h.CreateQuoteHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.POST("/lifecycle/quotes/submit", h.SubmitQuoteApprovalHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.POST("/lifecycle/quotes/approve", h.ApproveQuoteHandler, middleware.ModuleClearanceMiddleware(database, "tasks:approve"))
	api.POST("/lifecycle/projects", h.CreateProjectHandler, middleware.ModuleClearanceMiddleware(database, "tasks:create"))
	api.POST("/lifecycle/projects/task", h.LinkProjectTaskHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/lifecycle/evidence", h.AddProjectEvidenceHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/lifecycle/satisfaction", h.AddCustomerSatisfactionHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/execution/assign", h.CreateProjectAssignmentHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/execution/schedule", h.CreateProjectScheduleHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/execution/bom", h.CreateProjectBOMHandler, middleware.ModuleClearanceMiddleware(database, "inventory:write"))
	api.POST("/execution/complete/submit", h.SubmitCompletionReviewHandler, middleware.ModuleClearanceMiddleware(database, "tasks:write"))
	api.POST("/execution/complete/review", h.ReviewCompletionHandler, middleware.ModuleClearanceMiddleware(database, "tasks:approve"))
	api.POST("/users/reset-password", h.ResetPasswordHandler, middleware.ModuleClearanceMiddleware(database, "users:*"))

	// 7. Start server gracefully
	go func() {
		if err := e.Start(":8080"); err != nil {
			log.Printf("Echo server shut down: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shut down the server and consumer
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Gracefully shutting down Echo Server and background consumers...")
	ctxShutDown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if err := e.Shutdown(ctxShutDown); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
}

func setupStream(js nats.JetStreamContext, streamName string, streamSubjects []string) {
	_, err := js.StreamInfo(streamName)
	if err != nil {
		log.Printf("Stream %s not found, creating: %v", streamName, err)
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     streamName,
			Subjects: streamSubjects,
			Storage:  nats.FileStorage,
		})
		if err != nil {
			log.Printf("Warning: Failed to create stream %s: %v", streamName, err)
		}
	}
}

func setupConsumer(js nats.JetStreamContext, streamName, consumerName, filterSubject string) {
	_, err := js.ConsumerInfo(streamName, consumerName)
	if err != nil {
		log.Printf("Consumer %s not found under stream %s, creating: %v", consumerName, streamName, err)
		_, err = js.AddConsumer(streamName, &nats.ConsumerConfig{
			Durable:       consumerName,
			FilterSubject: filterSubject,
			DeliverPolicy: nats.DeliverAllPolicy,
			AckPolicy:     nats.AckExplicitPolicy,
			MaxDeliver:    5,
		})
		if err != nil {
			log.Printf("Warning: Failed to create durable consumer %s: %v", consumerName, err)
		}
	}
}
