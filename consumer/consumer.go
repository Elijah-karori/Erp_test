package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"erp-event-bus/db"
	"erp-event-bus/types"
)

// StartERPProcessors launches the multi-tenant, hierarchy, and ABAC checking consumers
func StartERPProcessors(ctx context.Context, nc *nats.Conn, database *db.Database, sdb *db.SQLiteDB) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	// 1. INVENTORY Consumer: Creating serialized assets & allocating to technicians
	invCons, err := js.Consumer(ctx, "INVENTORY", "InventoryWorker")
	if err == nil {
		go consumeInventory(invCons, js, database, sdb)
	} else {
		log.Printf("Warning: Inventory consumer binding skipped: %v", err)
	}

	// 2. FINANCE Consumer: M-Pesa billing and credit management
	finCons, err := js.Consumer(ctx, "FINANCE", "FinanceWorker")
	if err == nil {
		go consumeFinance(finCons, js, database, sdb)
	} else {
		log.Printf("Warning: Finance consumer binding skipped: %v", err)
	}

	// 3. TASKS Consumer: Technician timesheets with hierarchical approvals
	taskCons, err := js.Consumer(ctx, "TASKS", "TaskTimesheetWorker")
	if err == nil {
		go consumeTasks(taskCons, js, database, sdb)
	} else {
		log.Printf("Warning: Tasks consumer binding skipped: %v", err)
	}

	return nil
}

// consumeInventory handles serialized creation and assignment
func consumeInventory(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB) {
	_, err := cons.Consume(func(msg jetstream.Msg) {
		tenantID := msg.Headers().Get(types.HeaderTenantID)
		userID := msg.Headers().Get(types.HeaderUserID)
		userRoles := msg.Headers().Get(types.HeaderUserRoles)

		if tenantID == "" || userID == "" {
			log.Printf("INVENTORY: Rejecting message lacking Tenant or User header.")
			msg.Term()
			return
		}

		subject := msg.Subject()
		switch subject {
		case "erp.inventory.item.cmd.create":
			var cmd types.CreateItemCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			// ABAC: Check write permission
			if !database.CheckPermission(userRoles, "inventory:write") && !database.CheckPermission(userRoles, "*") {
				log.Printf("INVENTORY ABAC: User %s lacks create permission", userID)
				msg.Term()
				return
			}

			itemID := "item_" + cmd.SerialNumber
			database.SaveInventoryItem(&types.InventoryItem{
				ID:           itemID,
				TenantID:     tenantID,
				Name:         cmd.Name,
				SerialNumber: cmd.SerialNumber,
				Status:       "In_Stock",
				Region:       msg.Headers().Get(types.HeaderUserRegion),
			})
			log.Printf("INVENTORY: Created serialized item %s under Tenant %s", itemID, tenantID)
			sdb.Log(tenantID, userID, "CreateItem_Success", fmt.Sprintf("Asset %s (SN: %s) created in SQLite", cmd.Name, cmd.SerialNumber))
			msg.Ack()

		case "erp.inventory.item.cmd.assign":
			var cmd types.AssignDeviceCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			item, err := database.GetInventoryItem(cmd.ItemID)
			if err != nil {
				msg.Term() // item non-existent
				return
			}

			// Multi-Tenant Isolation ABAC
			if item.TenantID != tenantID {
				log.Printf("SECURITY ATTACK: Tenant mismatch! User %s tried to touch another tenant's item", userID)
				sdb.Log(tenantID, userID, "SecurityViolation_CrossTenant", fmt.Sprintf("User tried to assign cross-tenant item %s", cmd.ItemID))
				msg.Term()
				return
			}

			// Validate technician belongs to same tenant
			tech, err := database.GetUser(cmd.UserID)
			if err != nil || tech.TenantID != tenantID {
				log.Printf("INVENTORY ABAC: User %s tried to assign to invalid user %s", userID, cmd.UserID)
				msg.Term()
				return
			}

			item.Status = "Assigned"
			item.AssignedTo = cmd.UserID
			database.SaveInventoryItem(item)

			log.Printf("INVENTORY: Serialized asset %s allocated to tech %s under tenant %s", item.ID, cmd.UserID, tenantID)
			sdb.Log(tenantID, userID, "AssignItem_Success", fmt.Sprintf("Asset %s allocated to tech %s", cmd.ItemID, cmd.UserID))
			msg.Ack()

		default:
			log.Printf("INVENTORY: Received unrecognized subject %s. Terminating.", subject)
			msg.Term()
		}
	})
	if err != nil {
		log.Printf("INVENTORY Consumer Error: %v", err)
	}
}

// consumeFinance handles partial payments, billing and credit limit allocations
func consumeFinance(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB) {
	_, err := cons.Consume(func(msg jetstream.Msg) {
		tenantID := msg.Headers().Get(types.HeaderTenantID)
		userID := msg.Headers().Get(types.HeaderUserID)

		if tenantID == "" || userID == "" {
			msg.Term()
			return
		}

		if msg.Subject() == "erp.finance.payment.cmd.record" {
			var cmd types.RecordPaymentCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			inv, err := database.GetInvoice(cmd.InvoiceID)
			if err != nil {
				msg.Term()
				return
			}

			// Multi-tenant check
			if inv.TenantID != tenantID {
				log.Printf("FINANCE ABAC: Multi-tenant boundary breach attempted by User %s", userID)
				msg.Term()
				return
			}

			// Record Payment
			pmtID := "pmt_" + cmd.Reference
			payment := &types.Payment{
				ID:            pmtID,
				TenantID:      tenantID,
				InvoiceID:     cmd.InvoiceID,
				Amount:        cmd.Amount,
				PaymentMethod: cmd.PaymentMethod,
				Reference:     cmd.Reference,
				Date:          time.Now(),
			}
			database.SavePayment(payment)

			// Update Invoice (Partial payments & immediate confirmation alerts)
			inv.PaidAmount += cmd.Amount
			inv.BalanceAmount = inv.TotalAmount - inv.PaidAmount
			if inv.BalanceAmount <= 0 {
				inv.Status = "Paid"
			} else {
				inv.Status = "Partially_Paid"
			}
			database.SaveInvoice(inv)

			log.Printf("FINANCE: Applied payment %s KSh %.2f to invoice %s under tenant %s. New Status: %s",
				pmtID, cmd.Amount, inv.ID, tenantID, inv.Status)

			sdb.Log(tenantID, userID, "Payment_Reconciled", fmt.Sprintf("M-Pesa payment of KSh %.2f applied to invoice %s (Ref: %s). New invoice status: %s", cmd.Amount, inv.ID, cmd.Reference, inv.Status))

			// Fast confirmation event trigger to customer
			publishImmediateSMSConfirmation(js, tenantID, inv.CustomerID, cmd.Amount, inv.BalanceAmount)

			msg.Ack()
		}
	})
	if err != nil {
		log.Printf("FINANCE Consumer Error: %v", err)
	}
}

// consumeTasks resolves timesheet submissions based on user/role superiors checks
func consumeTasks(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB) {
	_, err := cons.Consume(func(msg jetstream.Msg) {
		tenantID := msg.Headers().Get(types.HeaderTenantID)
		userID := msg.Headers().Get(types.HeaderUserID)

		if tenantID == "" || userID == "" {
			msg.Term()
			return
		}

		if msg.Subject() == "erp.tasks.timesheet.cmd.approve" {
			var cmd types.ApproveTimesheetCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			ts, err := database.GetTimesheet(cmd.TimesheetID)
			if err != nil {
				msg.Term()
				return
			}

			// Multi-tenant check
			if ts.TenantID != tenantID {
				msg.Term()
				return
			}

			// Hierarchy check: Only superior managers can approve subordinate technician timesheets
			subordinate, err := database.GetUser(ts.UserID)
			if err != nil {
				msg.Term()
				return
			}

			// Retrieve approving user details to assert superior role
			approver, err := database.GetUser(userID)
			if err != nil {
				msg.Term()
				return
			}

			if !database.IsSubordinate(approver.RoleName, subordinate.RoleName) {
				log.Printf("SECURITY VIOLATION: Role %s attempted to approve timesheet of peer/superior role %s without authority",
					approver.RoleName, subordinate.RoleName)
				sdb.Log(tenantID, userID, "SecurityViolation_Hierarchy", fmt.Sprintf("User tried to approve timesheet %s without authority", cmd.TimesheetID))
				msg.Term()
				return
			}

			ts.Status = "Approved"
			ts.ApprovedBy = userID
			database.SaveTimesheet(ts)

			log.Printf("TASKS: Timesheet %s successfully approved by superior %s under tenant %s", ts.ID, userID, tenantID)
			sdb.Log(tenantID, userID, "ApproveTimesheet_Success", fmt.Sprintf("Approved timesheet %s for subordinate technician %s", cmd.TimesheetID, ts.UserID))
			msg.Ack()
		}
	})
	if err != nil {
		log.Printf("TASKS Consumer Error: %v", err)
	}
}

func publishImmediateSMSConfirmation(js jetstream.JetStream, tenantID, customerID string, amount float64, balance float64) {
	log.Printf("[SMS GATEWAY ALERT] Sending SMS to Customer %s for Tenant %s: 'KSh %.2f received. Current Outstanding Balance: KSh %.2f. Service activated.'",
		customerID, tenantID, amount, balance)
}
