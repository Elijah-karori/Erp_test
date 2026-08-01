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
	"erp-event-bus/handler"
	"erp-event-bus/middleware"
)

func main() {
	// 1. Initialize NATS server connection
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	nc, err := nats.Connect(natsURL, nats.Timeout(10*time.Second))
	if err != nil {
		log.Fatalf("Failed to connect to NATS server: %v", err)
	}
	defer nc.Close()

	// 2. Setup JetStream Stream Topology (and create stream "SALES" if missing)
	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("Failed to retrieve JetStream context: %v", err)
	}

	// Setup Multi-Tenant Streams: INVENTORY, FINANCE, TASKS
	setupStream(js, "INVENTORY", "erp.inventory.>")
	setupStream(js, "FINANCE", "erp.finance.>")
	setupStream(js, "TASKS", "erp.tasks.>")

	// Setup Durable Consumers
	setupConsumer(js, "INVENTORY", "InventoryWorker", "erp.inventory.item.cmd.>")
	setupConsumer(js, "FINANCE", "FinanceWorker", "erp.finance.payment.cmd.>")
	setupConsumer(js, "TASKS", "TaskTimesheetWorker", "erp.tasks.timesheet.cmd.>")

	// 4. Initialize SQLite Persistent Log Database
	sqliteDB, err := db.InitSQLite("erp.db")
	if err != nil {
		log.Fatalf("Failed to initialize SQLite persistent database: %v", err)
	}

	// Initialize In-Memory Database
	database := db.NewDatabase()

	// 5. Initialize downstream JetStream Consumer in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = consumer.StartERPProcessors(ctx, nc, database, sqliteDB)

	// 6. Initialize Echo Server
	e := echo.New()
	e.Use(echo_middleware.Logger())
	e.Use(echo_middleware.Recover())

	// UI Dashboard Endpoint (No Auth header required for page load itself)
	uiHandler := handler.NewUIHandler(database, sqliteDB)
	e.GET("/", uiHandler.ServeDashboard)

	// Set up mock auth middleware on all API group requests
	api := e.Group("/api")
	api.Use(middleware.MockAuthMiddleware())

	// State and live log endpoints for UI rendering
	api.GET("/state", uiHandler.GetState)
	api.GET("/logs", uiHandler.GetLogs)
	api.POST("/rbac/update", uiHandler.UpdateRBAC, middleware.ModuleClearanceMiddleware(database, "users:*"))
	e.GET("/api/exports/excel", uiHandler.ExportLogsExcel) // Export route direct download

	h := handler.NewERPHandler(nc, js, sqliteDB)

	// Inventory Endpoints: (Hierarchy Checks via middleware permission levels)
	api.POST("/inventory", h.CreateInventoryItemHandler, middleware.ModuleClearanceMiddleware(database, "inventory:write"))
	api.POST("/inventory/assign", h.AssignDeviceHandler, middleware.ModuleClearanceMiddleware(database, "inventory:*"))

	// Finance Endpoints:
	api.POST("/finance/payments", h.RecordPaymentHandler, middleware.ModuleClearanceMiddleware(database, "finance:write"))

	// Tasks Endpoints:
	api.POST("/tasks/timesheets/approve", h.ApproveTimesheetHandler, middleware.ModuleClearanceMiddleware(database, "timesheets:approve"))

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

func setupStream(js nats.JetStreamContext, streamName, streamSubject string) {
	_, err := js.StreamInfo(streamName)
	if err != nil {
		log.Printf("Stream %s not found, creating: %v", streamName, err)
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     streamName,
			Subjects: []string{streamSubject},
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
