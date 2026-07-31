package consumer

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"erp-event-bus/db"
	"erp-event-bus/types"
)

// StartOrderProcessor sets up the modern JetStream consumer loop for order approval commands
func StartOrderProcessor(ctx context.Context, nc *nats.Conn, database *db.Database) error {
	// Initialize JetStream
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	// Retrieve the Durable consumer
	cons, err := js.Consumer(ctx, "SALES", "OrderApprovalWorker")
	if err != nil {
		log.Printf("Consumer binding failed: %v", err)
		return err
	}

	// Consume messages with callback
	_, err = cons.Consume(func(msg jetstream.Msg) {
		// Extract identity context propagated via NATS Headers
		userID := msg.Headers().Get(types.HeaderUserID)
		userRegion := msg.Headers().Get(types.HeaderUserRegion)

		// A. Validate mandatory headers existence
		if userID == "" || userRegion == "" {
			log.Printf("SECURITY ALERT: Missing mandatory identity headers on message. Discarding.")
			msg.Term() // Terminal error: Missing authentication context is forbidden
			return
		}

		// B. Parse payload
		var cmd types.OrderCommand
		if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
			log.Printf("JSON unmarshal error from user %s: %v. Discarding.", userID, err)
			msg.Term() // Terminal error: Bad payload formatting will never succeed on retry
			return
		}

		// C. Retrieve resource state from DB
		order, err := database.GetOrder(cmd.OrderID)
		if err != nil {
			log.Printf("DB error fetching order %s: %v", cmd.OrderID, err)
			// Decide on Nak or Term based on error type. If order is truly not found, it is a terminal failure.
			if err.Error() == "order not found" {
				msg.Term() // Terminal error: Order does not exist
			} else {
				msg.Nak() // Transient error: Database might have experienced network issues
			}
			return
		}

		// D. Execute ABAC region policy rule check
		if order.Region != userRegion {
			log.Printf("SECURITY AUDIT: User %s (Region: %s) tried to approve order %s belonging to Region %s",
				userID, userRegion, cmd.OrderID, order.Region)

			// Publish an event to the security team's audit stream
			publishAuditAlert(js, userID, cmd.OrderID, userRegion, order.Region, "Unauthorized Region Access")

			msg.Term() // Terminal error: User lacked proper regional attributes
			return
		}

		// E. Execute Business Logic (e.g. Save Approved Status to Database)
		err = database.UpdateOrderStatus(cmd.OrderID, "APPROVED")
		if err != nil {
			log.Printf("Failed to update status for order %s: %v. Retrying...", cmd.OrderID, err)
			msg.Nak() // Transient error: Database lock or infrastructure issue. Nak to retry later.
			return
		}

		// F. Acknowledge success
		msg.Ack()
		log.Printf("Order %s successfully approved by %s in region %s", cmd.OrderID, userID, userRegion)
	})

	return err
}

// publishAuditAlert maps security violations to types.AuditAlert and publishes to erp.security.audit.v1.failed_access subject
func publishAuditAlert(js jetstream.JetStream, userID, orderID, userRegion, resourceRegion, reason string) {
	alert := types.AuditAlert{
		UserID:    userID,
		OrderID:   orderID,
		Reason:    reason,
		Violation: "User region " + userRegion + " does not match order region " + resourceRegion,
	}

	payload, err := json.Marshal(alert)
	if err != nil {
		log.Printf("Failed to marshal security audit alert: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = js.Publish(ctx, "erp.security.audit.v1.failed_access", payload)
	if err != nil {
		log.Printf("Failed to publish security alert event to audit stream: %v", err)
	} else {
		log.Printf("Security alert event successfully dispatched to erp.security.audit.v1.failed_access")
	}
}
