package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"erp-event-bus/db"
	"erp-event-bus/email"
	"erp-event-bus/types"
)

// StartERPProcessors launches the multi-tenant, hierarchy, and ABAC checking consumers
func StartERPProcessors(ctx context.Context, nc *nats.Conn, database *db.Database, sdb *db.SQLiteDB, emailSvc *email.EmailService) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	// 1. INVENTORY Consumer: Creating serialized assets & allocating to technicians
	invCons, err := js.Consumer(ctx, "INVENTORY", "InventoryWorker")
	if err == nil {
		go consumeInventory(invCons, js, database, sdb, nc)
	} else {
		log.Printf("Warning: Inventory consumer binding skipped: %v", err)
	}

	// 2. FINANCE Consumer: M-Pesa billing and credit management
	finCons, err := js.Consumer(ctx, "FINANCE", "FinanceWorker")
	if err == nil {
		go consumeFinance(finCons, js, database, sdb, nc)
	} else {
		log.Printf("Warning: Finance consumer binding skipped: %v", err)
	}

	// 3. TASKS Consumer: Technician timesheets with hierarchical approvals
	taskCons, err := js.Consumer(ctx, "TASKS", "TaskTimesheetWorker")
	if err == nil {
		go consumeTasks(taskCons, js, database, sdb, nc, emailSvc)
	} else {
		log.Printf("Warning: Tasks consumer binding skipped: %v", err)
	}

	return nil
}

func publishSecurityAlert(nc *nats.Conn, tenantID, userID, reason, violation string) {
	alert := types.AuditAlert{
		UserID:    userID,
		Reason:    reason,
		Violation: violation,
	}
	payload, _ := json.Marshal(alert)
	msg := nats.NewMsg("erp.security.audit.v1.failed_access")
	msg.Header.Set(types.HeaderTenantID, tenantID)
	msg.Header.Set(types.HeaderUserID, userID)
	msg.Data = payload
	_ = nc.PublishMsg(msg)
}

// consumeInventory handles serialized creation and assignment
func consumeInventory(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB, nc *nats.Conn) {
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
			if !database.CheckPermission(tenantID, userRoles, "inventory:write") && !database.CheckPermission(tenantID, userRoles, "*") {
				log.Printf("INVENTORY ABAC: User %s lacks create permission", userID)
				publishSecurityAlert(nc, tenantID, userID, "UnprivilegedInventoryCreation", "User lacks inventory:write privilege")
				msg.Term()
				return
			}

			itemID := "item_" + cmd.SerialNumber
			database.SaveInventoryItem(&types.InventoryItem{
				ID:               itemID,
				TenantID:         tenantID,
				Name:             cmd.Name,
				SerialNumber:     cmd.SerialNumber,
				Status:           "In_Stock",
				Region:           msg.Headers().Get(types.HeaderUserRegion),
				ReorderThreshold: cmd.ReorderThreshold,
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
				publishSecurityAlert(nc, tenantID, userID, "CrossTenantInventoryAccess", fmt.Sprintf("User tried to assign cross-tenant item %s", cmd.ItemID))
				msg.Term()
				return
			}

			// Validate technician belongs to same tenant
			tech, err := database.GetUser(cmd.UserID)
			if err != nil || tech.TenantID != tenantID {
				log.Printf("INVENTORY ABAC: User %s tried to assign to invalid user %s", userID, cmd.UserID)
				publishSecurityAlert(nc, tenantID, userID, "InvalidAllocationTarget", fmt.Sprintf("User tried to assign item to non-existent or cross-tenant technician %s", cmd.UserID))
				msg.Term()
				return
			}

			err = database.AllocateInventoryItemTransaction(cmd.ItemID, cmd.UserID)
			if err != nil {
				log.Printf("INVENTORY ABAC: Allocation failed: %v", err)
				msg.Term()
				return
			}

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
func consumeFinance(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB, nc *nats.Conn) {
	_, err := cons.Consume(func(msg jetstream.Msg) {
		tenantID := msg.Headers().Get(types.HeaderTenantID)
		userID := msg.Headers().Get(types.HeaderUserID)

		if tenantID == "" || userID == "" {
			msg.Term()
			return
		}

		subject := msg.Subject()
		switch subject {
		case "erp.finance.payment.cmd.record":
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
				publishSecurityAlert(nc, tenantID, userID, "CrossTenantFinanceAccess", fmt.Sprintf("User tried to record payment on cross-tenant invoice %s", cmd.InvoiceID))
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

			// Update Invoice (Partial payments & immediate confirmation alerts) via thread-safe transaction
			updatedInv, err := database.ApplyPaymentTransaction(cmd.InvoiceID, cmd.Amount)
			if err != nil {
				log.Printf("FINANCE ABAC: Apply payment failed: %v", err)
				msg.Term()
				return
			}

			log.Printf("FINANCE: Applied payment %s KSh %.2f to invoice %s under tenant %s. New Status: %s",
				pmtID, cmd.Amount, updatedInv.ID, tenantID, updatedInv.Status)

			sdb.Log(tenantID, userID, "Payment_Reconciled", fmt.Sprintf("M-Pesa payment of KSh %.2f applied to invoice %s (Ref: %s). New invoice status: %s", cmd.Amount, updatedInv.ID, cmd.Reference, updatedInv.Status))

			// Fast confirmation event trigger to customer
			publishImmediateSMSConfirmation(tenantID, updatedInv.CustomerID, cmd.Amount, updatedInv.BalanceAmount)
			msg.Ack()

		case "erp.customers.crm.cmd.create":
			var cmd types.CreateCustomerCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			database.CreateCustomerTransaction(cmd.ID, tenantID, cmd.Name, cmd.Phone, cmd.Email)
			sdb.Log(tenantID, userID, "CreateCustomer_Success", fmt.Sprintf("Customer %s registered under tenant %s", cmd.Name, tenantID))
			msg.Ack()
		}
	})
	if err != nil {
		log.Printf("FINANCE Consumer Error: %v", err)
	}
}

// consumeTasks resolves timesheet submissions based on user/role superiors checks
func consumeTasks(cons jetstream.Consumer, js jetstream.JetStream, database *db.Database, sdb *db.SQLiteDB, nc *nats.Conn, emailSvc *email.EmailService) {
	_, err := cons.Consume(func(msg jetstream.Msg) {
		tenantID := msg.Headers().Get(types.HeaderTenantID)
		userID := msg.Headers().Get(types.HeaderUserID)

		if tenantID == "" || userID == "" {
			msg.Term()
			return
		}

		subject := msg.Subject()
		switch subject {
		case "erp.tasks.timesheet.cmd.approve":
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

			if !database.IsSubordinate(tenantID, approver.RoleName, subordinate.RoleName) {
				log.Printf("SECURITY VIOLATION: Role %s attempted to approve timesheet of peer/superior role %s without authority",
					approver.RoleName, subordinate.RoleName)
				sdb.Log(tenantID, userID, "SecurityViolation_Hierarchy", fmt.Sprintf("User tried to approve timesheet %s without authority", cmd.TimesheetID))
				publishSecurityAlert(nc, tenantID, userID, "HierarchyViolation", fmt.Sprintf("Approver role %s is not superior to subordinate role %s", approver.RoleName, subordinate.RoleName))
				msg.Term()
				return
			}

			// Regional validation check: Approver region must match task region
			task, err := database.GetTask(ts.TaskID)
			if err != nil {
				msg.Term()
				return
			}

			if task.Region != approver.Region {
				log.Printf("SECURITY VIOLATION: Approver %s (region %s) tried to approve timesheet for task in region %s",
					approver.ID, approver.Region, task.Region)
				sdb.Log(tenantID, userID, "SecurityViolation_RegionMismatch", fmt.Sprintf("User tried to approve timesheet %s for task in another region", cmd.TimesheetID))
				publishSecurityAlert(nc, tenantID, userID, "RegionMismatchViolation", fmt.Sprintf("Approver region %s does not match task region %s", approver.Region, task.Region))
				msg.Term()
				return
			}

			err = database.ApproveTimesheetTransaction(cmd.TimesheetID, userID)
			if err != nil {
				log.Printf("TASKS ABAC: Timesheet approval failed: %v", err)
				msg.Term()
				return
			}

			log.Printf("TASKS: Timesheet %s successfully approved by superior %s under tenant %s", ts.ID, userID, tenantID)
			sdb.Log(tenantID, userID, "ApproveTimesheet_Success", fmt.Sprintf("Approved timesheet %s for subordinate technician %s", cmd.TimesheetID, ts.UserID))

			// Trigger approval notification email
			if emailSvc != nil {
				techUser, errUser := database.GetUser(ts.UserID)
				mgrUser, _ := database.GetUser(userID)
				if errUser == nil && techUser != nil {
					mgrName := "Manager"
					if mgrUser != nil {
						mgrName = mgrUser.Name
					}
					_ = emailSvc.SendApprovalNotification(techUser.Email, mgrName, "Timesheet", cmd.TimesheetID, "Approved")
				}
			}

			msg.Ack()

		case "erp.users.auth.cmd.reset_password":
			var cmd types.ResetPasswordCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			err := database.ResetPasswordTransaction(cmd.UserID, cmd.NewPassword)
			if err != nil {
				msg.Term()
				return
			}
			sdb.Log(tenantID, userID, "ResetPassword_Success", fmt.Sprintf("Credential password reset for user %s executed in SQLite", cmd.UserID))
			msg.Ack()

		case "erp.tasks.materials.cmd.request":
			var cmd types.SubmitMaterialRequestCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			reqID := "req_" + uuid.New().String()[:8]
			database.CreateMaterialRequestTransaction(reqID, tenantID, cmd.TaskID, userID, cmd.ItemName)
			sdb.Log(tenantID, userID, "MaterialRequest_Success", fmt.Sprintf("Technician material request %s logged", reqID))
			msg.Ack()

		case "erp.tasks.materials.cmd.approve":
			var cmd types.ApproveMaterialCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			updatedReq, procOrder, err := database.ApproveMaterialRequestTransaction(cmd.RequestID, tenantID, userID)
			if err != nil {
				msg.Term()
				return
			}

			if procOrder != nil {
				sdb.Log(tenantID, userID, "MaterialApproval_ProcurementEscalation", fmt.Sprintf("Material %s out of stock. Requisition escalated to Procurement order %s", updatedReq.ItemName, procOrder.ID))
			} else {
				sdb.Log(tenantID, userID, "MaterialApproval_Fulfilled", fmt.Sprintf("Material approved and serial asset %s issued automatically", updatedReq.AllocatedSN))
			}

			// Trigger approval email for materials
			if emailSvc != nil {
				reqUser, errUser := database.GetUser(updatedReq.RequesterID)
				leadUser, _ := database.GetUser(userID)
				if errUser == nil && reqUser != nil {
					leadName := "Team Lead"
					if leadUser != nil {
						leadName = leadUser.Name
					}
					statusStr := "Approved (Fulfilled with SN: " + updatedReq.AllocatedSN + ")"
					if procOrder != nil {
						statusStr = "Escalated to Procurement (Out of stock)"
					}
					_ = emailSvc.SendApprovalNotification(reqUser.Email, leadName, "Material Request", cmd.RequestID, statusStr)
				}
			}

			msg.Ack()

		case "erp.tasks.task.cmd.create":
			var cmd types.CreateTaskCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			// ABAC: Check write permission (tasks:create)
			userRoles := msg.Headers().Get(types.HeaderUserRoles)
			if !database.CheckPermission(tenantID, userRoles, "tasks:create") && !database.CheckPermission(tenantID, userRoles, "tasks:*") && !database.CheckPermission(tenantID, userRoles, "*") {
				log.Printf("TASKS ABAC: User %s lacks tasks:create permission", userID)
				publishSecurityAlert(nc, tenantID, userID, "UnprivilegedTaskCreation", "User lacks tasks:create privilege")
				msg.Term()
				return
			}

			assigner, err := database.GetUser(userID)
			if err != nil {
				msg.Term()
				return
			}

			database.SaveTask(&types.Task{
				ID:         cmd.ID,
				TenantID:   tenantID,
				Title:      cmd.Title,
				AssignedTo: cmd.AssignedTo,
				CreatedBy:  userID,
				Status:     "Pending",
				Region:     assigner.Region,
				DueDate:    cmd.DueDate,
				DependsOn:  cmd.DependsOn,
			})
			log.Printf("TASKS: Created task %s under Tenant %s", cmd.ID, tenantID)
			sdb.Log(tenantID, userID, "CreateTask_Success", fmt.Sprintf("Task %s created in SQLite", cmd.Title))
			msg.Ack()

		case "erp.tasks.task.cmd.update_status":
			var cmd types.UpdateTaskStatusCommand
			if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
				msg.Term()
				return
			}

			task, err := database.GetTask(cmd.TaskID)
			if err != nil {
				msg.Term()
				return
			}

			if task.TenantID != tenantID {
				publishSecurityAlert(nc, tenantID, userID, "CrossTenantAttack", fmt.Sprintf("User tried to modify cross-tenant task %s", cmd.TaskID))
				msg.Term()
				return
			}

			// Enforce depends_on check if transitioning to In_Progress
			if cmd.Status == "In_Progress" && task.DependsOn != "" {
				depTask, err := database.GetTask(task.DependsOn)
				if err == nil && depTask != nil {
					if depTask.Status != "Completed" && depTask.Status != "Approved" {
						log.Printf("TASKS ABAC: Task %s depends on %s which is not Completed/Approved (current: %s)", cmd.TaskID, task.DependsOn, depTask.Status)
						sdb.Log(tenantID, userID, "SecurityViolation_TaskDependency", fmt.Sprintf("Attempted to start task %s before dependency %s is completed", cmd.TaskID, task.DependsOn))
						publishSecurityAlert(nc, tenantID, userID, "TaskDependencyViolation", fmt.Sprintf("Task %s depends on unfinished task %s", cmd.TaskID, task.DependsOn))
						msg.Term()
						return
					}
				}
			}

			task.Status = cmd.Status
			database.SaveTask(task)
			log.Printf("TASKS: Updated task %s status to %s", task.ID, cmd.Status)
			sdb.Log(tenantID, userID, "UpdateTaskStatus_Success", fmt.Sprintf("Updated task %s status to %s", task.ID, cmd.Status))
			msg.Ack()
		}
	})
	if err != nil {
		log.Printf("TASKS Consumer Error: %v", err)
	}
}

func publishImmediateSMSConfirmation(tenantID, customerID string, amount float64, balance float64) {
	log.Printf("[SMS GATEWAY ALERT] Sending SMS to Customer %s for Tenant %s: 'KSh %.2f received. Current Outstanding Balance: KSh %.2f. Service activated.'",
		customerID, tenantID, amount, balance)
}
