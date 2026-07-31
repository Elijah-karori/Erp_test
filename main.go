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

	streamName := "SALES"
	streamSubject := "erp.sales.>"

	// Try to get the stream first
	_, err = js.StreamInfo(streamName)
	if err != nil {
		log.Printf("SALES stream not found, creating it: %v", err)
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     streamName,
			Subjects: []string{streamSubject},
			Storage:  nats.FileStorage,
		})
		if err != nil {
			log.Fatalf("Failed to add SALES stream: %v", err)
		}
	}

	// 3. Setup Durable Consumer "OrderApprovalWorker" bound to "erp.sales.order.cmd.approve"
	consumerName := "OrderApprovalWorker"
	_, err = js.ConsumerInfo(streamName, consumerName)
	if err != nil {
		log.Printf("OrderApprovalWorker consumer not found, creating durable consumer: %v", err)
		_, err = js.AddConsumer(streamName, &nats.ConsumerConfig{
			Durable:        consumerName,
			FilterSubject:  "erp.sales.order.cmd.approve",
			DeliverPolicy:  nats.DeliverAllPolicy,
			AckPolicy:      nats.AckExplicitPolicy,
			MaxDeliver:     5,
		})
		if err != nil {
			log.Fatalf("Failed to create Durable Consumer: %v", err)
		}
	}

	// 4. Initialize Database
	database := db.NewDatabase()

	// 5. Initialize downstream JetStream Consumer in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = consumer.StartOrderProcessor(ctx, nc, database)
	if err != nil {
		log.Fatalf("Failed to start JetStream Consumer Processor: %v", err)
	}

	// 6. Initialize Echo Server
	e := echo.New()
	e.Use(echo_middleware.Logger())
	e.Use(echo_middleware.Recover())

	// Set up mock auth middleware on all API group requests
	api := e.Group("/api")
	api.Use(middleware.MockAuthMiddleware())

	h := handler.NewOrderHandler(nc, js)

	// Create Order Endpoint (Edge RBAC allows sales_rep)
	api.POST("/orders", h.CreateOrderHandler, middleware.EdgeRBACMiddleware("sales_rep"))

	// Approve Order Endpoint (Edge RBAC allows manager)
	api.POST("/orders/approve", h.ApproveOrderHandler, middleware.EdgeRBACMiddleware("manager"))

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
