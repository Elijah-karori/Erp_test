package db

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"erp-event-bus/auth"
	"erp-event-bus/types"
)

//go:embed schema.sql
var SchemaSQL string

type Database struct {
	Pool *pgxpool.Pool
}

func mustHashSeedPassword(plaintext string) string {
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		log.Fatalf("failed to hash seed password: %v", err)
	}
	return hash
}

func NewDatabase(pool *pgxpool.Pool) *Database {
	db := &Database{Pool: pool}
	db.SeedIfNeeded()
	return db
}

func (d *Database) withTx(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Set tenant ID local session configuration for RLS
	if tenantID != "" {
		_, err = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)
		if err != nil {
			return err
		}
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (d *Database) CheckPermission(tenantID string, roleName string, permission string) bool {
	ctx := context.Background()
	var perms []string
	var parentRole *string

	current := roleName
	for i := 0; i < 5 && current != ""; i++ {
		err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, "SELECT permissions, parent_role FROM roles WHERE tenant_id = $1 AND name = $2", tenantID, current)
			return row.Scan(&perms, &parentRole)
		})
		if err != nil {
			break
		}

		for _, p := range perms {
			if p == "*" || p == permission {
				return true
			}
			if strings.HasSuffix(p, ":*") {
				prefix := p[:len(p)-2]
				if strings.HasPrefix(permission, prefix) {
					return true
				}
			}
		}

		if parentRole == nil {
			break
		}
		current = *parentRole
	}
	return false
}

func (d *Database) GetUser(userID string) (*types.User, error) {
	ctx := context.Background()
	var u types.User
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, name, email, role_name, region, password_hash, COALESCE(manager_id, '') FROM users WHERE id = $1", userID)
		return row.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash, &u.ManagerID)
	})
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) GetUserByEmail(email string) (*types.User, error) {
	ctx := context.Background()
	var u types.User
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, name, email, role_name, region, password_hash, COALESCE(manager_id, '') FROM users WHERE LOWER(email) = LOWER($1)", strings.TrimSpace(email))
		return row.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash, &u.ManagerID)
	})
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) IsSubordinate(tenantID string, managerRole, subordinateRole string) bool {
	if managerRole == subordinateRole {
		return true
	}
	ctx := context.Background()
	current := managerRole
	var parentRole *string

	for i := 0; i < 5 && current != ""; i++ {
		err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, "SELECT parent_role FROM roles WHERE tenant_id = $1 AND name = $2", tenantID, current)
			return row.Scan(&parentRole)
		})
		if err != nil {
			break
		}
		if parentRole == nil {
			break
		}
		if *parentRole == subordinateRole {
			return true
		}
		current = *parentRole
	}
	return false
}

func (d *Database) CheckSerialNumberExists(tenantID, serialNumber string) (bool, error) {
	ctx := context.Background()
	var exists bool
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inventory_items WHERE tenant_id = $1 AND serial_number = $2)", tenantID, serialNumber)
		return row.Scan(&exists)
	})
	return exists, err
}

func (d *Database) CheckReorderThresholdAndTriggerProcurement(ctx context.Context, tx pgx.Tx, tenantID string, itemName string) error {
	fn := func(transaction pgx.Tx) error {
		var threshold int
		err := transaction.QueryRow(ctx, "SELECT COALESCE(MAX(reorder_threshold), 0) FROM inventory_items WHERE tenant_id = $1 AND name = $2", tenantID, itemName).Scan(&threshold)
		if err != nil {
			return err
		}

		var inStockCount int
		err = transaction.QueryRow(ctx, "SELECT COUNT(*) FROM inventory_items WHERE tenant_id = $1 AND name = $2 AND status = 'In_Stock'", tenantID, itemName).Scan(&inStockCount)
		if err != nil {
			return err
		}

		if inStockCount <= threshold {
			var exists bool
			err = transaction.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM procurement_orders WHERE tenant_id = $1 AND item_name = $2 AND status = 'Bidding')", tenantID, itemName).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				procID := "proc_auto_" + uuid.New().String()[:8]
				_, err = transaction.Exec(ctx, `
					INSERT INTO procurement_orders (id, tenant_id, request_id, item_name, expected_time, status)
					VALUES ($1, $2, NULL, $3, $4, 'Bidding')`,
					procID, tenantID, itemName, time.Now().Add(10*24*time.Hour))
				if err != nil {
					return err
				}
			}
		}
		return nil
	}

	if tx != nil {
		return fn(tx)
	}

	return d.withTx(ctx, tenantID, func(transaction pgx.Tx) error {
		return fn(transaction)
	})
}

func (d *Database) SaveInventoryItem(item *types.InventoryItem) {
	ctx := context.Background()
	_ = d.withTx(ctx, item.TenantID, func(tx pgx.Tx) error {
		var currentStatus string
		_ = tx.QueryRow(ctx, "SELECT status FROM inventory_items WHERE id = $1", item.ID).Scan(&currentStatus)

		_, err := tx.Exec(ctx, `
			INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)
			ON CONFLICT (id) DO UPDATE SET
				name = $3,
				serial_number = $4,
				status = $5,
				assigned_to = NULLIF($6, ''),
				region = $7,
				reorder_threshold = $8`,
			item.ID, item.TenantID, item.Name, item.SerialNumber, item.Status, item.AssignedTo, item.Region, item.ReorderThreshold)
		if err != nil {
			return err
		}

		if currentStatus != "" && currentStatus != item.Status {
			_, _ = tx.Exec(ctx, `
				INSERT INTO inventory_history (tenant_id, item_id, from_status, to_status, changed_by, changed_at)
				VALUES ($1, $2, $3, $4, $5, NOW())`,
				item.TenantID, item.ID, currentStatus, item.Status, "SYSTEM")
		}

		return d.CheckReorderThresholdAndTriggerProcurement(ctx, tx, item.TenantID, item.Name)
	})
}

func (d *Database) GetInventoryItem(id string) (*types.InventoryItem, error) {
	ctx := context.Background()
	var item types.InventoryItem
	var assignedTo *string
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold FROM inventory_items WHERE id = $1", id)
		return row.Scan(&item.ID, &item.TenantID, &item.Name, &item.SerialNumber, &item.Status, &assignedTo, &item.Region, &item.ReorderThreshold)
	})
	if err != nil {
		return nil, err
	}
	if assignedTo != nil {
		item.AssignedTo = *assignedTo
	}
	return &item, nil
}

func (d *Database) SaveInvoice(invoice *types.Invoice) {
	ctx := context.Background()
	_ = d.withTx(ctx, invoice.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO UPDATE SET
				customer_id = $3,
				total_amount = $4,
				paid_amount = $5,
				balance_amount = $6,
				status = $7,
				region = $8`,
			invoice.ID, invoice.TenantID, invoice.CustomerID, invoice.TotalAmount, invoice.PaidAmount, invoice.BalanceAmount, invoice.Status, invoice.Region)
		return err
	})
}

func (d *Database) SaveInventoryItemDirect(tenantID, name, serialNumber string, reorderThreshold int, region string) error {
	ctx := context.Background()
	itemID := "item_man_" + uuid.NewString()[:8]
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, region, reorder_threshold)
			VALUES ($1, $2, $3, $4, 'In_Stock', $5, $6)`,
			itemID, tenantID, name, serialNumber, region, reorderThreshold)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO inventory_history (tenant_id, item_id, from_status, to_status, changed_by, changed_at)
			VALUES ($1, $2, 'None', 'In_Stock', 'Manual_Entry', NOW())`,
			tenantID, itemID)
		return err
	})
}

func (d *Database) UpdateInvoiceNoteUsageType(noteID, tenantID, usageType, customerID string) error {
	ctx := context.Background()
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var status string
		var invoiceID *string

		if usageType == "Internal" {
			status = "Paid_Usage_Support"
			_, err := tx.Exec(ctx, `
				UPDATE invoice_notes
				SET usage_type = $1, status = $2, invoice_id = NULL
				WHERE id = $3 AND tenant_id = $4`,
				usageType, status, noteID, tenantID)
			return err
		}

		// Installation or Customer_Broken usage
		status = "Pending_Payment"
		invID := "inv_" + noteID
		invoiceID = &invID

		// Auto-generate customer Invoice record
		// Check if customer exists, else fallback
		var custID string
		if customerID != "" {
			custID = customerID
		} else {
			_ = tx.QueryRow(ctx, "SELECT id FROM customers WHERE tenant_id = $1 LIMIT 1", tenantID).Scan(&custID)
		}

		if custID != "" {
			_, _ = tx.Exec(ctx, `
				INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region)
				VALUES ($1, $2, $3, 12500.00, 0.00, 12500.00, 'Draft', 'Nairobi')
				ON CONFLICT (id) DO NOTHING`,
				invID, tenantID, custID)
		}

		_, err := tx.Exec(ctx, `
			UPDATE invoice_notes
			SET usage_type = $1, status = $2, invoice_id = $3
			WHERE id = $4 AND tenant_id = $5`,
			usageType, status, invoiceID, noteID, tenantID)
		if err != nil {
			return err
		}
		var taskID string
		_ = tx.QueryRow(ctx, `SELECT COALESCE(task_id,'') FROM invoice_notes WHERE id=$1 AND tenant_id=$2`, noteID, tenantID).Scan(&taskID)
		_, err = tx.Exec(ctx, `INSERT INTO business_ledger_entries(id,tenant_id,transaction_id,task_id,invoice_id,entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,reference) VALUES($1,$2,$3,NULLIF($4,''),$5,'INVOICE',$5,'REVENUE','CREDIT',$6,1,$6,'customer-charge') ON CONFLICT DO NOTHING`, uuid.NewString(), tenantID, "invoice_"+invID, taskID, invID, 12500.00)
		return err
	})
}

func (d *Database) ConfirmProcurementReceipt(procID, tenantID, confirmedBy, photoURL string) error {
	ctx := context.Background()
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var itemName string
		err := tx.QueryRow(ctx, "SELECT item_name FROM procurement_orders WHERE id = $1 AND tenant_id = $2", procID, tenantID).Scan(&itemName)
		if err != nil {
			return fmt.Errorf("procurement order not found: %w", err)
		}

		// Update procurement status
		_, err = tx.Exec(ctx, `
			UPDATE procurement_orders
			SET status = 'Completed', barcode_photo_url = $1, confirmed_at = NOW(), confirmed_by = $2
			WHERE id = $3 AND tenant_id = $4`,
			photoURL, confirmedBy, procID, tenantID)
		if err != nil {
			return err
		}

		// Auto Barcode Verification & Extraction
		// Extract verification token / barcode (simulated image processing)
		shortUUID := uuid.NewString()[:8]
		extractedSN := "SN-AUTO-PROC-" + shortUUID
		itemID := "item_proc_" + shortUUID

		// Insert automatic inventory item
		_, err = tx.Exec(ctx, `
			INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, region, reorder_threshold)
			VALUES ($1, $2, $3, $4, 'In_Stock', 'Nairobi', 2)`,
			itemID, tenantID, itemName, extractedSN)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO inventory_history (tenant_id, item_id, from_status, to_status, changed_by, changed_at)
			VALUES ($1, $2, 'None', 'In_Stock', $3, NOW())`,
			tenantID, itemID, confirmedBy)
		return err
	})
}

func (d *Database) CreateSupportTicket(ticket *types.SupportTicket) error {
	ctx := context.Background()
	return d.withTx(ctx, ticket.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO support_tickets (id, tenant_id, customer_id, title, description, status, task_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8)`,
			ticket.ID, ticket.TenantID, ticket.CustomerID, ticket.Title, ticket.Description, ticket.Status, ticket.TaskID, ticket.CreatedAt)
		return err
	})
}

func (d *Database) GetSupportTicket(id string) (*types.SupportTicket, error) {
	ctx := context.Background()
	var t types.SupportTicket
	var taskID *string
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, customer_id, title, description, status, task_id, created_at FROM support_tickets WHERE id = $1", id)
		return row.Scan(&t.ID, &t.TenantID, &t.CustomerID, &t.Title, &t.Description, &t.Status, &taskID, &t.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	if taskID != nil {
		t.TaskID = *taskID
	}
	return &t, nil
}

func (d *Database) ConvertTicketToTaskTransaction(ticketID, tenantID, taskID string) error {
	ctx := context.Background()
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE support_tickets SET status = 'Converted', task_id = $1 WHERE id = $2 AND tenant_id = $3", taskID, ticketID, tenantID)
		return err
	})
}

func (d *Database) IsSubordinateRecursive(tenantID string, managerID, subordinateID string) bool {
	if managerID == subordinateID {
		return true
	}
	ctx := context.Background()
	current := subordinateID
	var mID *string

	// Max 10 levels deep to prevent cycles or deep recursion issues
	for i := 0; i < 10 && current != ""; i++ {
		err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, "SELECT manager_id FROM users WHERE tenant_id = $1 AND id = $2", tenantID, current)
			return row.Scan(&mID)
		})
		if err != nil || mID == nil {
			break
		}
		if *mID == managerID {
			return true
		}
		current = *mID
	}
	return false
}

func (d *Database) GetInvoice(id string) (*types.Invoice, error) {
	ctx := context.Background()
	var inv types.Invoice
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region FROM invoices WHERE id = $1", id)
		return row.Scan(&inv.ID, &inv.TenantID, &inv.CustomerID, &inv.TotalAmount, &inv.PaidAmount, &inv.BalanceAmount, &inv.Status, &inv.Region)
	})
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (d *Database) SavePayment(pmt *types.Payment) {
	_, _ = d.SavePaymentIdempotent(pmt)
}

// SavePaymentIdempotent inserts a payment exactly once per tenant/reference.
// The bool is false when the reference was already recorded, preventing a
// redelivered JetStream command from incrementing the invoice twice.
func (d *Database) SavePaymentIdempotent(pmt *types.Payment) (bool, error) {
	ctx := context.Background()
	inserted := false
	err := d.withTx(ctx, pmt.TenantID, func(tx pgx.Tx) error {
		if pmt.Reference != "" {
			var existing string
			err := tx.QueryRow(ctx, `SELECT id FROM payments WHERE tenant_id=$1 AND reference=$2`, pmt.TenantID, pmt.Reference).Scan(&existing)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		result, err := tx.Exec(ctx, `INSERT INTO payments (id, tenant_id, invoice_id, amount, payment_method, reference, paid_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, pmt.ID, pmt.TenantID, pmt.InvoiceID, pmt.Amount, pmt.PaymentMethod, pmt.Reference, pmt.Date)
		if err != nil {
			return err
		}
		inserted = result.RowsAffected() == 1
		if inserted {
			_, _ = tx.Exec(ctx, `
				UPDATE invoice_notes
				SET status = 'Cleared_Paid', payment_id = $1
				WHERE invoice_id = $2 AND tenant_id = $3`,
				pmt.ID, pmt.InvoiceID, pmt.TenantID)
		}
		return nil
	})
	return inserted, err
}

func (d *Database) GetTask(id string) (*types.Task, error) {
	ctx := context.Background()
	var t types.Task
	var assignedTo, dependsOn *string
	var dueDate *time.Time
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on, COALESCE(customer_id, ''), COALESCE(project_id, '') FROM tasks WHERE id = $1", id)
		return row.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn, &t.CustomerID, &t.ProjectID)
	})
	if err != nil {
		return nil, err
	}
	if assignedTo != nil {
		t.AssignedTo = *assignedTo
	}
	if dependsOn != nil {
		t.DependsOn = *dependsOn
	}
	t.DueDate = dueDate
	return &t, nil
}

func (d *Database) SaveTask(task *types.Task) {
	ctx := context.Background()
	_ = d.withTx(ctx, task.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on, customer_id, project_id)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''))
			ON CONFLICT (id) DO UPDATE SET
				title = $3,
				assigned_to = NULLIF($4, ''),
				created_by = $5,
				status = $6,
				region = $7,
				due_date = $8,
				depends_on = NULLIF($9, ''),
				customer_id = NULLIF($10, ''),
				project_id = NULLIF($11, '')`,
			task.ID, task.TenantID, task.Title, task.AssignedTo, task.CreatedBy, task.Status, task.Region, task.DueDate, task.DependsOn, task.CustomerID, task.ProjectID)
		if err != nil {
			return err
		}

		// Reconcile Invoice Notes: if the task is finished but payment is still pending, move to Reconciliation_Started
		if task.Status == "Completed" || task.Status == "Approved" {
			_, _ = tx.Exec(ctx, `
				UPDATE invoice_notes
				SET status = 'Reconciliation_Started'
				WHERE task_id = $1 AND tenant_id = $2 AND status = 'Pending_Payment'`,
				task.ID, task.TenantID)
		}
		return nil
	})
}

func (d *Database) GetTimesheet(id string) (*types.Timesheet, error) {
	ctx := context.Background()
	var ts types.Timesheet
	var approvedBy *string
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, task_id, user_id, hours, worked_on, status, approved_by FROM timesheets WHERE id = $1", id)
		return row.Scan(&ts.ID, &ts.TenantID, &ts.TaskID, &ts.UserID, &ts.Hours, &ts.Date, &ts.Status, &approvedBy)
	})
	if err != nil {
		return nil, err
	}
	if approvedBy != nil {
		ts.ApprovedBy = *approvedBy
	}
	return &ts, nil
}

func (d *Database) SaveTimesheet(ts *types.Timesheet) {
	ctx := context.Background()
	_ = d.withTx(ctx, ts.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO timesheets (id, tenant_id, task_id, user_id, hours, worked_on, status, approved_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
			ON CONFLICT (id) DO UPDATE SET
				task_id = $3,
				user_id = $4,
				hours = $5,
				worked_on = $6,
				status = $7,
				approved_by = NULLIF($8, '')`,
			ts.ID, ts.TenantID, ts.TaskID, ts.UserID, ts.Hours, ts.Date, ts.Status, ts.ApprovedBy)
		return err
	})
}

func (d *Database) CreateTenant(id, name string) {
	ctx := context.Background()
	_ = d.withTx(ctx, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING", id, name)
		return err
	})
}

func (d *Database) CreateUser(id, tenantID, name, email, roleName, region, passwordHash string) error {
	ctx := context.Background()
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var tExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tenants WHERE id = $1)", tenantID).Scan(&tExists)
		if !tExists {
			return fmt.Errorf("tenant %s does not exist", tenantID)
		}

		var rExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM roles WHERE tenant_id = $1 AND name = $2)", tenantID, roleName).Scan(&rExists)
		if !rExists {
			return fmt.Errorf("role %s does not exist", roleName)
		}

		var emailExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1))", strings.TrimSpace(email)).Scan(&emailExists)
		if emailExists {
			return errors.New("email already registered")
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO users (id, tenant_id, name, email, role_name, region, password_hash, manager_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)`,
			id, tenantID, name, strings.ToLower(strings.TrimSpace(email)), roleName, region, passwordHash)
		return err
	})
}

func (d *Database) RegisterUserTransaction(id, tenantID, tenantName, name, email, region, passwordHash string) (*types.User, error) {
	ctx := context.Background()
	var user types.User
	user.ID = id
	user.TenantID = tenantID
	user.Name = name
	user.Email = strings.ToLower(strings.TrimSpace(email))
	user.Region = region
	user.PasswordHash = passwordHash

	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		var emailExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1))", user.Email).Scan(&emailExists)
		if emailExists {
			return errors.New("email already registered")
		}

		roleName := "field_technician"
		var tenantExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tenants WHERE id = $1)", tenantID).Scan(&tenantExists)
		if !tenantExists {
			_, err := tx.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ($1, $2)", tenantID, tenantName)
			if err != nil {
				return err
			}

			defaultRoles := []struct {
				Name        string
				ParentRole  string
				Permissions []string
			}{
				{"field_technician", "", []string{"inventory:read", "tasks:read", "timesheets:submit"}},
				{"finance_officer", "", []string{"finance:read", "finance:write"}},
				{"manager", "field_technician", []string{"finance:read", "tasks:create", "tasks:approve", "timesheets:approve"}},
				{"tenant_admin", "manager", []string{"inventory:*", "finance:*", "tasks:*", "users:*"}},
			}
			for _, r := range defaultRoles {
				_, err = tx.Exec(ctx, "INSERT INTO roles (tenant_id, name, parent_role, permissions) VALUES ($1, $2, NULLIF($3, ''), $4)",
					tenantID, r.Name, r.ParentRole, r.Permissions)
				if err != nil {
					return err
				}
			}
			roleName = "tenant_admin"
		}

		user.RoleName = roleName

		_, err := tx.Exec(ctx, `
			INSERT INTO users (id, tenant_id, name, email, role_name, region, password_hash, manager_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)`,
			user.ID, user.TenantID, user.Name, user.Email, user.RoleName, user.Region, user.PasswordHash)
		return err
	})

	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (d *Database) ResetPasswordTransaction(userID, newPasswordHash string) error {
	ctx := context.Background()
	return d.withTx(ctx, "", func(tx pgx.Tx) error {
		var exists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)", userID).Scan(&exists)
		if !exists {
			return errors.New("user not found")
		}
		_, err := tx.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", newPasswordHash, userID)
		return err
	})
}

func (d *Database) CreateMaterialRequestTransaction(id, tenantID, taskID, requesterID, itemName string) {
	ctx := context.Background()
	_ = d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO material_requests (id, tenant_id, task_id, requester_id, item_name, status, created_at)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5, 'Pending_Leader_Approval', $6)
			ON CONFLICT (id) DO NOTHING`,
			id, tenantID, taskID, requesterID, itemName, time.Now())
		return err
	})
}

// FulfillMaterialRequestTransaction is the business transaction released by the
// governance outbox. It creates the requisition if needed, locks a matching
// in-stock serialized unit, allocates it to the technician, writes inventory
// history and the invoice usage note, or escalates to procurement when no unit
// is available. The request ID is the idempotency key.
func (d *Database) FulfillMaterialRequestTransaction(ctx context.Context, tenantID string, cmd types.FulfillMaterialRequestCommand, actor string) (*types.MaterialRequest, *types.ProcurementOrder, error) {
	var req types.MaterialRequest
	var proc *types.ProcurementOrder
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var existingStatus string
		var existingRequester, existingItem string
		var existingTask *string
		err := tx.QueryRow(ctx, `SELECT status, requester_id, item_name, task_id FROM material_requests WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, cmd.RequestID, tenantID).
			Scan(&existingStatus, &existingRequester, &existingItem, &existingTask)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err != nil {
			_, err = tx.Exec(ctx, `INSERT INTO material_requests(id,tenant_id,task_id,requester_id,item_name,status) VALUES($1,$2,NULLIF($3,''),$4,$5,'PROCESSING')`, cmd.RequestID, tenantID, cmd.TaskID, cmd.RequesterID, cmd.ItemName)
			if err != nil {
				return err
			}
			existingStatus, existingRequester, existingItem = "PROCESSING", cmd.RequesterID, cmd.ItemName
			existingTask = nil
			if cmd.TaskID != "" {
				t := cmd.TaskID
				existingTask = &t
			}
		}
		if existingStatus == "Fulfilled" || existingStatus == "Issued" {
			req = types.MaterialRequest{ID: cmd.RequestID, TenantID: tenantID, TaskID: cmd.TaskID, RequesterID: existingRequester, ItemName: existingItem, Status: existingStatus}
			if existingTask != nil {
				req.TaskID = *existingTask
			}
			_ = tx.QueryRow(ctx, `SELECT COALESCE(allocated_sn,'') FROM material_requests WHERE id=$1 AND tenant_id=$2`, cmd.RequestID, tenantID).Scan(&req.AllocatedSN)
			return nil
		}
		if existingRequester == "" {
			existingRequester = cmd.RequesterID
		}
		if existingItem == "" {
			existingItem = cmd.ItemName
		}
		if existingTask == nil && cmd.TaskID != "" {
			t := cmd.TaskID
			existingTask = &t
		}

		var itemID, serial, itemRegion string
		var unitCost float64
		err = tx.QueryRow(ctx, `
			SELECT id, serial_number, region, unit_cost FROM inventory_items
			WHERE tenant_id=$1 AND name=$2 AND status='In_Stock'
			ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, tenantID, existingItem).Scan(&itemID, &serial, &itemRegion, &unitCost)
		if err == nil {
			if _, err = tx.Exec(ctx, `UPDATE inventory_items SET status='Assigned', assigned_to=$1 WHERE id=$2 AND tenant_id=$3 AND status='In_Stock'`, existingRequester, itemID, tenantID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO inventory_reservations(id,tenant_id,request_id,item_id,reserved_for,status) VALUES($1,$2,$3,$4,$5,'ISSUED') ON CONFLICT (tenant_id,request_id) DO UPDATE SET item_id=EXCLUDED.item_id,reserved_for=EXCLUDED.reserved_for,status='ISSUED'`, uuid.NewString(), tenantID, cmd.RequestID, itemID, existingRequester); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO inventory_history(tenant_id,item_id,from_status,to_status,changed_by,changed_at) VALUES($1,$2,'In_Stock','Assigned',$3,now())`, tenantID, itemID, actor); err != nil {
				return err
			}
			tid := ""
			if existingTask != nil {
				tid = *existingTask
			}
			if _, err = tx.Exec(ctx, `UPDATE material_requests SET status='Fulfilled', allocated_sn=$1, task_id=NULLIF($2,''), requester_id=$3, item_name=$4 WHERE id=$5 AND tenant_id=$6`, serial, tid, existingRequester, existingItem, cmd.RequestID, tenantID); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO invoice_notes(id,tenant_id,request_id,item_name,allocated_sn,task_id,requester_id,usage_type,status) VALUES($1,$2,$3,$4,$5,$6,$7,'Internal','Paid_Usage_Support') ON CONFLICT(id) DO NOTHING`, "note_"+cmd.RequestID, tenantID, cmd.RequestID, existingItem, serial, tid, existingRequester)
			if err != nil {
				return err
			}
			txid := "mat_" + cmd.RequestID
			_, err = tx.Exec(ctx, `INSERT INTO business_ledger_entries(id,tenant_id,transaction_id,task_id,entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,reference,actor_id) VALUES($1,$2,$3,NULLIF($4,''),'MATERIAL',$5,'MATERIAL_COST','DEBIT',$6,1,$6,$7,$8) ON CONFLICT DO NOTHING`, uuid.NewString(), tenantID, txid, tid, cmd.RequestID, unitCost, serial, actor)
			if err != nil {
				return err
			}
			req = types.MaterialRequest{ID: cmd.RequestID, TenantID: tenantID, TaskID: tid, RequesterID: existingRequester, ItemName: existingItem, Status: "Fulfilled", AllocatedSN: serial, Timestamp: time.Now()}
			return d.CheckReorderThresholdAndTriggerProcurement(ctx, tx, tenantID, existingItem)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		procID := "proc_" + cmd.RequestID
		if _, err = tx.Exec(ctx, `UPDATE material_requests SET status='Procuring', task_id=NULLIF($1,''), requester_id=$2, item_name=$3 WHERE id=$4 AND tenant_id=$5`, cmd.TaskID, existingRequester, existingItem, cmd.RequestID, tenantID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO procurement_orders(id,tenant_id,request_id,item_name,expected_time,status) VALUES($1,$2,$3,$4,$5,'Bidding') ON CONFLICT(id) DO UPDATE SET status='Bidding'`, procID, tenantID, cmd.RequestID, existingItem, time.Now().Add(10*24*time.Hour)); err != nil {
			return err
		}
		tid := ""
		if existingTask != nil {
			tid = *existingTask
		}
		req = types.MaterialRequest{ID: cmd.RequestID, TenantID: tenantID, TaskID: tid, RequesterID: existingRequester, ItemName: existingItem, Status: "Procuring", Timestamp: time.Now()}
		proc = &types.ProcurementOrder{ID: procID, TenantID: tenantID, RequestID: cmd.RequestID, ItemName: existingItem, ExpectedTime: time.Now().Add(10 * 24 * time.Hour), Status: "Bidding"}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &req, proc, nil
}

func (d *Database) ApproveMaterialRequestTransaction(id, tenantID, approverID string) (*types.MaterialRequest, *types.ProcurementOrder, error) {
	// Compatibility wrapper for older direct consumers. New governance paths use
	// FulfillMaterialRequestTransaction after workflow execution.
	return d.FulfillMaterialRequestTransaction(context.Background(), tenantID, types.FulfillMaterialRequestCommand{RequestID: id}, approverID)
}

func (d *Database) CreateCustomerTransaction(id, tenantID, name, phone, email string) {
	ctx := context.Background()
	_ = d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO customers (id, tenant_id, name, phone, email, dispatch_status)
			VALUES ($1, $2, $3, $4, $5, 'Pending')`,
			id, tenantID, name, phone, email)
		return err
	})
}

func (d *Database) UpdateCustomerDeviceTransaction(id, deviceID, invoiceID, dispatchStatus string) error {
	ctx := context.Background()
	var tenantID string
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT tenant_id FROM customers WHERE id = $1", id).Scan(&tenantID)
		if err != nil {
			return fmt.Errorf("customer %s not found", id)
		}

		_, err = tx.Exec(ctx, `
			UPDATE customers
			SET device_id = NULLIF($1, ''), invoice_id = NULLIF($2, ''), dispatch_status = $3
			WHERE id = $4 AND tenant_id = $5`,
			deviceID, invoiceID, dispatchStatus, id, tenantID)
		return err
	})
	return err
}

func (d *Database) UpdateRolePermissions(tenantID string, roleName string, permissions []string) {
	ctx := context.Background()
	_ = d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE roles SET permissions = $1 WHERE tenant_id = $2 AND name = $3", permissions, tenantID, roleName)
		return err
	})
}

func (d *Database) AllocateInventoryItemTransaction(id string, assignedTo string) error {
	ctx := context.Background()
	return d.withTx(ctx, "", func(tx pgx.Tx) error {
		var tenantID, fromStatus, itemName string
		err := tx.QueryRow(ctx, "SELECT tenant_id, status, name FROM inventory_items WHERE id = $1", id).Scan(&tenantID, &fromStatus, &itemName)
		if err != nil {
			return fmt.Errorf("item %s not found", id)
		}

		_, err = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, "UPDATE inventory_items SET status = 'Assigned', assigned_to = $1 WHERE id = $2 AND tenant_id = $3", assignedTo, id, tenantID)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO inventory_history (tenant_id, item_id, from_status, to_status, changed_by, changed_at)
			VALUES ($1, $2, $3, 'Assigned', $4, $5)`,
			tenantID, id, fromStatus, assignedTo, time.Now())
		if err != nil {
			return err
		}

		return d.CheckReorderThresholdAndTriggerProcurement(ctx, tx, tenantID, itemName)
	})
}

func (d *Database) ApplyPaymentTransaction(id string, amount float64) (*types.Invoice, error) {
	ctx := context.Background()
	var inv types.Invoice
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		var tenantID string
		err := tx.QueryRow(ctx, "SELECT tenant_id FROM invoices WHERE id = $1", id).Scan(&tenantID)
		if err != nil {
			return fmt.Errorf("invoice %s not found", id)
		}

		_, err = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)
		if err != nil {
			return err
		}

		err = tx.QueryRow(ctx, `
			UPDATE invoices
			SET paid_amount = paid_amount + $1,
			    balance_amount = total_amount - (paid_amount + $1),
			    status = CASE WHEN total_amount - (paid_amount + $1) <= 0 THEN 'Paid' ELSE 'Partially_Paid' END
			WHERE id = $2 AND tenant_id = $3
			RETURNING id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region`,
			amount, id, tenantID).
			Scan(&inv.ID, &inv.TenantID, &inv.CustomerID, &inv.TotalAmount, &inv.PaidAmount, &inv.BalanceAmount, &inv.Status, &inv.Region)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO business_ledger_entries(id,tenant_id,transaction_id,invoice_id,entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,reference) VALUES($1,$2,$3,$4,'PAYMENT',$4,'ACCOUNTS_RECEIVABLE','CREDIT',$5,1,$5,$6) ON CONFLICT DO NOTHING`, uuid.NewString(), tenantID, "payment_"+id+"_"+fmt.Sprintf("%.2f", amount), id, amount, "invoice-payment")
		return err
	})
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (d *Database) ApproveTimesheetTransaction(id string, approvedBy string) error {
	ctx := context.Background()
	return d.withTx(ctx, "", func(tx pgx.Tx) error {
		var tenantID string
		err := tx.QueryRow(ctx, "SELECT tenant_id FROM timesheets WHERE id = $1", id).Scan(&tenantID)
		if err != nil {
			return fmt.Errorf("timesheet %s not found", id)
		}

		_, err = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, "UPDATE timesheets SET status = 'Approved', approved_by = $1 WHERE id = $2 AND tenant_id = $3", approvedBy, id, tenantID)
		if err != nil {
			return err
		}
		var taskID, userID string
		var hours float64
		if err := tx.QueryRow(ctx, `SELECT task_id,user_id,hours FROM timesheets WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&taskID, &userID, &hours); err != nil {
			return err
		}
		var rate float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT hourly_rate FROM labor_rates WHERE tenant_id=$1 AND user_id=$2 AND active=true ORDER BY created_at DESC LIMIT 1),(SELECT hourly_rate FROM labor_rates WHERE tenant_id=$1 AND role_name=(SELECT role_name FROM users WHERE id=$2) AND active=true ORDER BY created_at DESC LIMIT 1),0)`, tenantID, userID).Scan(&rate); err != nil {
			return err
		}
		amount := rate * hours
		_, err = tx.Exec(ctx, `INSERT INTO business_ledger_entries(id,tenant_id,transaction_id,task_id,entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,reference,actor_id) VALUES($1,$2,$3,$4,'TIMESHEET',$5,'LABOR_COST','DEBIT',$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, uuid.NewString(), tenantID, "labor_"+id, taskID, id, amount, hours, rate, "approved-timesheet", approvedBy)
		return err
	})
}

func (d *Database) GetRolesForTenant(tenantID string) map[string][]string {
	ctx := context.Background()
	roles := make(map[string][]string)
	_ = d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT name, permissions FROM roles WHERE tenant_id = $1", tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var name string
			var perms []string
			if err := rows.Scan(&name, &perms); err == nil {
				roles[name] = perms
			}
		}
		return nil
	})
	return roles
}

func (d *Database) GetStateForTenant(tenantID, userID, roleName string) map[string]interface{} {
	ctx := context.Background()

	var (
		mu                sync.Mutex
		tenants           = make(map[string]*types.Tenant)
		users             = make(map[string]*types.User)
		inventory         = make(map[string]*types.InventoryItem)
		invoices          = make(map[string]*types.Invoice)
		tasks             = make(map[string]*types.Task)
		timesheets        = make(map[string]*types.Timesheet)
		customers         = make(map[string]*types.Customer)
		materialRequests  = make(map[string]*types.MaterialRequest)
		procurementOrders = make(map[string]*types.ProcurementOrder)
		overdueTasks      = make(map[string]*types.Task)
		invitations       = make(map[string]*types.Invitation)
		supportTickets    = make(map[string]*types.SupportTicket)
	)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, name FROM tenants WHERE id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t types.Tenant
				if err := rows.Scan(&t.ID, &t.Name); err == nil {
					mu.Lock()
					tenants[t.ID] = &t
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, name, email, role_name, region, password_hash, COALESCE(manager_id, '') FROM users WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var u types.User
				if err := rows.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash, &u.ManagerID); err == nil {
					mu.Lock()
					users[u.ID] = &u
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, email, name, role_name, region, COALESCE(manager_id, ''), token, status, created_at FROM invitations WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var inv types.Invitation
				if err := rows.Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Name, &inv.RoleName, &inv.Region, &inv.ManagerID, &inv.Token, &inv.Status, &inv.CreatedAt); err == nil {
					mu.Lock()
					invitations[inv.ID] = &inv
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold FROM inventory_items WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var item types.InventoryItem
				var assignedTo *string
				if err := rows.Scan(&item.ID, &item.TenantID, &item.Name, &item.SerialNumber, &item.Status, &assignedTo, &item.Region, &item.ReorderThreshold); err == nil {
					if assignedTo != nil {
						item.AssignedTo = *assignedTo
					}
					mu.Lock()
					inventory[item.ID] = &item
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region FROM invoices WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var inv types.Invoice
				if err := rows.Scan(&inv.ID, &inv.TenantID, &inv.CustomerID, &inv.TotalAmount, &inv.PaidAmount, &inv.BalanceAmount, &inv.Status, &inv.Region); err == nil {
					mu.Lock()
					invoices[inv.ID] = &inv
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on, COALESCE(customer_id, ''), COALESCE(project_id, '') FROM tasks WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t types.Task
				var assignedTo, dependsOn *string
				var dueDate *time.Time
				if err := rows.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn, &t.CustomerID, &t.ProjectID); err == nil {
					if assignedTo != nil {
						t.AssignedTo = *assignedTo
					}
					if dependsOn != nil {
						t.DependsOn = *dependsOn
					}
					t.DueDate = dueDate
					mu.Lock()
					tasks[t.ID] = &t
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, task_id, user_id, hours, worked_on, status, approved_by FROM timesheets WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ts types.Timesheet
				var approvedBy *string
				if err := rows.Scan(&ts.ID, &ts.TenantID, &ts.TaskID, &ts.UserID, &ts.Hours, &ts.Date, &ts.Status, &approvedBy); err == nil {
					if approvedBy != nil {
						ts.ApprovedBy = *approvedBy
					}
					mu.Lock()
					timesheets[ts.ID] = &ts
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, name, phone, email, device_id, invoice_id, dispatch_status FROM customers WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var c types.Customer
				var deviceID, invoiceID *string
				if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Phone, &c.Email, &deviceID, &invoiceID, &c.DispatchStatus); err == nil {
					if deviceID != nil {
						c.DeviceID = *deviceID
					}
					if invoiceID != nil {
						c.InvoiceID = *invoiceID
					}
					mu.Lock()
					customers[c.ID] = &c
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, task_id, requester_id, item_name, status, allocated_sn, created_at FROM material_requests WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var m types.MaterialRequest
				var allocatedSN, taskID *string
				if err := rows.Scan(&m.ID, &m.TenantID, &taskID, &m.RequesterID, &m.ItemName, &m.Status, &allocatedSN, &m.Timestamp); err == nil {
					if allocatedSN != nil {
						m.AllocatedSN = *allocatedSN
					}
					if taskID != nil {
						m.TaskID = *taskID
					}
					mu.Lock()
					materialRequests[m.ID] = &m
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, request_id, item_name, expected_time, status, barcode_photo_url, confirmed_at, confirmed_by FROM procurement_orders WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p types.ProcurementOrder
				var requestID, barcodePhotoURL, confirmedBy *string
				var confirmedAt, expectedTime *time.Time
				if err := rows.Scan(&p.ID, &p.TenantID, &requestID, &p.ItemName, &expectedTime, &p.Status, &barcodePhotoURL, &confirmedAt, &confirmedBy); err == nil {
					if requestID != nil {
						p.RequestID = *requestID
					}
					if expectedTime != nil {
						p.ExpectedTime = *expectedTime
					}
					if barcodePhotoURL != nil {
						p.BarcodePhotoURL = *barcodePhotoURL
					}
					if confirmedAt != nil {
						p.ConfirmedAt = *confirmedAt
					}
					if confirmedBy != nil {
						p.ConfirmedBy = *confirmedBy
					}
					mu.Lock()
					procurementOrders[p.ID] = &p
					mu.Unlock()
				} else {
					log.Printf("[Bootstrap Warning] failed to scan procurement order row: %v", err)
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on, COALESCE(customer_id, ''), COALESCE(project_id, '') FROM tasks WHERE tenant_id = $1 AND due_date < CURRENT_DATE AND status NOT IN ('Completed', 'Approved')", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t types.Task
				var assignedTo, dependsOn *string
				var dueDate *time.Time
				if err := rows.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn, &t.CustomerID, &t.ProjectID); err == nil {
					if assignedTo != nil {
						t.AssignedTo = *assignedTo
					}
					if dependsOn != nil {
						t.DependsOn = *dependsOn
					}
					t.DueDate = dueDate
					mu.Lock()
					overdueTasks[t.ID] = &t
					mu.Unlock()
				}
			}
			return nil
		})
	})

	invoiceNotes := make(map[string]*types.InvoiceNote)

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, `
				SELECT id, tenant_id, request_id, item_name, allocated_sn, COALESCE(task_id, ''), requester_id, usage_type, status, COALESCE(invoice_id, ''), COALESCE(payment_id, ''), created_at
				FROM invoice_notes WHERE tenant_id = $1`, tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var n types.InvoiceNote
				if err := rows.Scan(&n.ID, &n.TenantID, &n.RequestID, &n.ItemName, &n.AllocatedSN, &n.TaskID, &n.RequesterID, &n.UsageType, &n.Status, &n.InvoiceID, &n.PaymentID, &n.CreatedAt); err == nil {
					mu.Lock()
					invoiceNotes[n.ID] = &n
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, customer_id, title, description, status, COALESCE(task_id, ''), created_at FROM support_tickets WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ticket types.SupportTicket
				if err := rows.Scan(&ticket.ID, &ticket.TenantID, &ticket.CustomerID, &ticket.Title, &ticket.Description, &ticket.Status, &ticket.TaskID, &ticket.CreatedAt); err == nil {
					mu.Lock()
					supportTickets[ticket.ID] = &ticket
					mu.Unlock()
				}
			}
			return nil
		})
	})

	if err := g.Wait(); err != nil {
		log.Printf("GetStateForTenant background query error: %v", err)
	}

	// In-memory hierarchical permission-based filtering
	if roleName != "tenant_admin" && roleName != "" {
		isSubordinate := func(mgrID, subID string) bool {
			if mgrID == subID {
				return true
			}
			current := subID
			for i := 0; i < 10 && current != ""; i++ {
				u, exists := users[current]
				if !exists || u.ManagerID == "" {
					break
				}
				if u.ManagerID == mgrID {
					return true
				}
				current = u.ManagerID
			}
			return false
		}

		// Filter tasks
		filteredTasks := make(map[string]*types.Task)
		for k, t := range tasks {
			if t.AssignedTo == userID || t.CreatedBy == userID || (roleName == "manager" && isSubordinate(userID, t.AssignedTo)) {
				filteredTasks[k] = t
			}
		}
		tasks = filteredTasks

		// Filter timesheets
		filteredTimesheets := make(map[string]*types.Timesheet)
		for k, ts := range timesheets {
			if ts.UserID == userID || (roleName == "manager" && isSubordinate(userID, ts.UserID)) {
				filteredTimesheets[k] = ts
			}
		}
		timesheets = filteredTimesheets

		// Filter overdue tasks
		filteredOverdue := make(map[string]*types.Task)
		for k, t := range overdueTasks {
			if t.AssignedTo == userID || t.CreatedBy == userID || (roleName == "manager" && isSubordinate(userID, t.AssignedTo)) {
				filteredOverdue[k] = t
			}
		}
		overdueTasks = filteredOverdue
	}

	return map[string]interface{}{
		"tenants":            tenants,
		"users":              users,
		"inventory":          inventory,
		"invoices":           invoices,
		"tasks":              tasks,
		"timesheets":         timesheets,
		"customers":          customers,
		"material_requests":  materialRequests,
		"procurement_orders": procurementOrders,
		"overdue_tasks":      overdueTasks,
		"invitations":        invitations,
		"support_tickets":    supportTickets,
		"invoice_notes":      invoiceNotes,
	}
}

// CreateWorkflowRequest atomically creates a workflow. Execution is deliberately
// separated from creation so no business command can escape before approval.
func (d *Database) CreateWorkflowRequest(ctx context.Context, tenantID, id, entityType, entityID, workflowKey, requestedBy, subject string, payload []byte) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_instances
			(id,tenant_id,entity_type,entity_id,workflow_key,requested_by,status,command_subject,command_payload)
			VALUES($1,$2,$3,$4,$5,$6,'REQUESTED',$7,$8::jsonb)`,
			id, tenantID, entityType, entityID, workflowKey, requestedBy, subject, string(payload))
		return err
	})
}

// ExecuteWorkflowTransaction records EXECUTED and creates its outbox event in
// one PostgreSQL transaction. This closes the DB-commit/NATS-publish gap.
func (d *Database) ExecuteWorkflowTransaction(ctx context.Context, tenantID, instanceID, actor, reason string, headers map[string]string) (string, error) {
	outboxID := uuid.NewString()
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var subject string
		var payload []byte
		var requested, status string
		if err := tx.QueryRow(ctx, `SELECT command_subject, command_payload, requested_by, status FROM workflow_instances WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, instanceID, tenantID).Scan(&subject, &payload, &requested, &status); err != nil {
			return err
		}
		if status != "APPROVED" {
			return fmt.Errorf("workflow %s is not approved", instanceID)
		}
		if requested == actor {
			return fmt.Errorf("segregation of duties: requester cannot execute own workflow")
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_instances SET status='EXECUTED', executed_by=$1, reason=$2, updated_at=now() WHERE id=$3 AND tenant_id=$4`, actor, reason, instanceID, tenantID); err != nil {
			return err
		}
		headerJSON := "{}"
		if len(headers) > 0 {
			b, err := json.Marshal(headers)
			if err != nil {
				return err
			}
			headerJSON = string(b)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflow_decisions(id,instance_id,tenant_id,actor_id,action,reason)
			VALUES($1,$2,$3,$4,'execute',$5)`, uuid.NewString(), instanceID, tenantID, actor, reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO outbox_events (id,tenant_id,aggregate_type,aggregate_id,subject,payload,headers)
			VALUES($1,$2,'WORKFLOW',$3,$4,$5::jsonb,$6::jsonb)`, outboxID, tenantID, instanceID, subject, string(payload), headerJSON)
		return err
	})
	if err != nil {
		return "", err
	}
	return outboxID, nil
}

// ClaimInbox records a delivery before business processing. A completed entry
// is a duplicate and can be acknowledged immediately. PROCESSING entries are
// reclaimable after a timeout, allowing recovery from consumer crashes.
func (d *Database) ClaimInbox(ctx context.Context, consumerName, messageID, tenantID, subject string) (bool, error) {
	var claimed bool
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM inbox_messages WHERE message_id=$1 FOR UPDATE`, messageID).Scan(&status)
		if err == nil {
			if status == "PROCESSED" {
				claimed = false
				return nil
			}
			result, updateErr := tx.Exec(ctx, `UPDATE inbox_messages SET status='PROCESSING', attempts=attempts+1, locked_at=now(), last_error=NULL WHERE message_id=$1 AND (locked_at IS NULL OR locked_at < now() - interval '5 minutes')`, messageID)
			if updateErr != nil {
				return updateErr
			}
			claimed = result.RowsAffected() == 1
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO inbox_messages(message_id,consumer_name,tenant_id,subject,locked_at) VALUES($1,$2,$3,$4,now())`, messageID, consumerName, tenantID, subject)
		claimed = err == nil
		return err
	})
	return claimed, err
}

func (d *Database) CompleteInbox(ctx context.Context, messageID string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE inbox_messages SET status='PROCESSED', processed_at=now(), completed_at=now(), locked_at=NULL, last_error=NULL WHERE message_id=$1`, messageID)
	return err
}

func (d *Database) FailInbox(ctx context.Context, messageID string, cause error) error {
	_, err := d.Pool.Exec(ctx, `UPDATE inbox_messages SET status='PROCESSING', last_error=$2 WHERE message_id=$1`, messageID, cause.Error())
	return err
}

func (d *Database) ListLedger(ctx context.Context, tenantID, taskID string, limit int) ([]types.BusinessLedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT id,tenant_id,transaction_id,COALESCE(task_id,''),COALESCE(invoice_id,''),entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,currency,COALESCE(reference,''),COALESCE(actor_id,''),created_at,COALESCE(reversal_of,'') FROM business_ledger_entries WHERE tenant_id=$1`
	args := []any{tenantID}
	if taskID != "" {
		q += " AND task_id=$2"
		args = append(args, taskID)
	}
	q += " ORDER BY created_at DESC LIMIT $" + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := d.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []types.BusinessLedgerEntry{}
	for rows.Next() {
		var e types.BusinessLedgerEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.TransactionID, &e.TaskID, &e.InvoiceID, &e.EntityType, &e.EntityID, &e.Account, &e.EntryType, &e.Amount, &e.Quantity, &e.UnitCost, &e.Currency, &e.Reference, &e.ActorID, &e.CreatedAt, &e.ReversalOf); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *Database) GetProfitability(ctx context.Context, tenantID string) ([]types.ProfitabilitySummary, error) {
	rows, err := d.Pool.Query(ctx, `SELECT t.id,t.title,COALESCE(SUM(CASE WHEN l.account='REVENUE' AND l.entry_type='CREDIT' THEN l.amount ELSE 0 END),0),COALESCE(SUM(CASE WHEN l.account='MATERIAL_COST' AND l.entry_type='DEBIT' THEN l.amount ELSE 0 END),0),COALESCE(SUM(CASE WHEN l.account='LABOR_COST' AND l.entry_type='DEBIT' THEN l.amount ELSE 0 END),0) FROM tasks t LEFT JOIN business_ledger_entries l ON l.tenant_id=t.tenant_id AND l.task_id=t.id WHERE t.tenant_id=$1 GROUP BY t.id,t.title ORDER BY t.created_at DESC LIMIT 200`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []types.ProfitabilitySummary{}
	for rows.Next() {
		var p types.ProfitabilitySummary
		if err := rows.Scan(&p.TaskID, &p.TaskTitle, &p.Revenue, &p.MaterialCost, &p.LaborCost); err != nil {
			return nil, err
		}
		p.GrossMargin = p.Revenue - p.MaterialCost - p.LaborCost
		if p.Revenue > 0 {
			p.MarginPct = p.GrossMargin / p.Revenue * 100
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *Database) ReverseLedgerEntry(ctx context.Context, tenantID, entryID, actor, reason string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var e types.BusinessLedgerEntry
		if err := tx.QueryRow(ctx, `SELECT id,transaction_id,COALESCE(task_id,''),COALESCE(invoice_id,''),entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,currency,COALESCE(reference,''),COALESCE(actor_id,''),created_at,COALESCE(reversal_of,'') FROM business_ledger_entries WHERE id=$1 AND tenant_id=$2`, entryID, tenantID).Scan(&e.ID, &e.TransactionID, &e.TaskID, &e.InvoiceID, &e.EntityType, &e.EntityID, &e.Account, &e.EntryType, &e.Amount, &e.Quantity, &e.UnitCost, &e.Currency, &e.Reference, &e.ActorID, &e.CreatedAt, &e.ReversalOf); err != nil {
			return err
		}
		if e.ReversalOf != "" {
			return fmt.Errorf("entry already reversed")
		}
		_, err := tx.Exec(ctx, `INSERT INTO business_ledger_entries(id,tenant_id,transaction_id,task_id,invoice_id,entity_type,entity_id,account,entry_type,amount,quantity,unit_cost,currency,reference,actor_id,reversal_of) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,CASE WHEN $9='DEBIT' THEN 'CREDIT' ELSE 'DEBIT' END,$10,$11,$12,$13,$14,$15,$16)`, uuid.NewString(), tenantID, "REV_"+e.TransactionID, e.TaskID, e.InvoiceID, e.EntityType, e.EntityID, e.Account, e.EntryType, e.Amount, e.Quantity, e.UnitCost, e.Currency, "reversal: "+reason, actor, e.ID)
		return err
	})
}

func (d *Database) ensureCustomerProjectLifecycle(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS leads (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 name text not null, phone text, email text, source text not null default 'Unknown',
 status text not null default 'New', owner_id text references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_leads_tenant_status ON leads(tenant_id,status);
CREATE TABLE IF NOT EXISTS quotes (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 customer_id text not null references customers(id) on delete cascade, lead_id text references leads(id) on delete set null,
 title text not null, amount numeric(14,2) not null default 0, status text not null default 'Draft',
 valid_until timestamptz, created_by text not null references users(id), approved_by text references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_quotes_tenant_status ON quotes(tenant_id,status);
CREATE TABLE IF NOT EXISTS projects (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 customer_id text not null references customers(id) on delete cascade, quote_id text references quotes(id) on delete set null,
 name text not null, status text not null default 'Planned', region text not null,
 start_date date, target_date date, created_by text not null references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_projects_tenant_status ON projects(tenant_id,status);
CREATE INDEX IF NOT EXISTS idx_projects_customer ON projects(tenant_id,customer_id);
CREATE TABLE IF NOT EXISTS project_tasks (
 project_id text not null references projects(id) on delete cascade, task_id text not null references tasks(id) on delete cascade,
 tenant_id text not null references tenants(id) on delete cascade, created_at timestamptz not null default now(),
 primary key(project_id,task_id)
);
CREATE TABLE IF NOT EXISTS project_evidence (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text not null references projects(id) on delete cascade, task_id text references tasks(id) on delete set null,
 evidence_type text not null, url text not null, note text, captured_by text not null references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_project_evidence_project ON project_evidence(tenant_id,project_id);
CREATE TABLE IF NOT EXISTS customer_satisfaction (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 customer_id text not null references customers(id) on delete cascade, project_id text references projects(id) on delete set null,
 rating int not null check(rating between 1 and 5), comment text, created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_customer_satisfaction_customer ON customer_satisfaction(tenant_id,customer_id);
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS project_id text REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS customer_id text REFERENCES customers(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS project_id text REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE leads ENABLE ROW LEVEL SECURITY; ALTER TABLE leads FORCE ROW LEVEL SECURITY;
ALTER TABLE quotes ENABLE ROW LEVEL SECURITY; ALTER TABLE quotes FORCE ROW LEVEL SECURITY;
ALTER TABLE projects ENABLE ROW LEVEL SECURITY; ALTER TABLE projects FORCE ROW LEVEL SECURITY;
ALTER TABLE project_tasks ENABLE ROW LEVEL SECURITY; ALTER TABLE project_tasks FORCE ROW LEVEL SECURITY;
ALTER TABLE project_evidence ENABLE ROW LEVEL SECURITY; ALTER TABLE project_evidence FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_satisfaction ENABLE ROW LEVEL SECURITY; ALTER TABLE customer_satisfaction FORCE ROW LEVEL SECURITY;
UPDATE roles SET permissions = array_append(permissions,'tasks:write') WHERE name='field_technician' AND NOT ('tasks:write'=ANY(permissions));
UPDATE roles SET permissions = array_append(permissions,'tasks:write') WHERE name='manager' AND NOT ('tasks:write'=ANY(permissions));
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='leads' AND policyname='lifecycle_leads_tenant') THEN CREATE POLICY lifecycle_leads_tenant ON leads USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='quotes' AND policyname='lifecycle_quotes_tenant') THEN CREATE POLICY lifecycle_quotes_tenant ON quotes USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='projects' AND policyname='lifecycle_projects_tenant') THEN CREATE POLICY lifecycle_projects_tenant ON projects USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='project_tasks' AND policyname='lifecycle_project_tasks_tenant') THEN CREATE POLICY lifecycle_project_tasks_tenant ON project_tasks USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='project_evidence' AND policyname='lifecycle_project_evidence_tenant') THEN CREATE POLICY lifecycle_project_evidence_tenant ON project_evidence USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='customer_satisfaction' AND policyname='lifecycle_customer_satisfaction_tenant') THEN CREATE POLICY lifecycle_customer_satisfaction_tenant ON customer_satisfaction USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
END $$;`)
	return err
}

func (d *Database) ensureFieldTelemetry(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS field_attendance (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 user_id text not null references users(id), clock_in_at timestamptz not null,
 clock_in_lat numeric(10,7) not null, clock_in_lon numeric(10,7) not null,
 clock_in_photo_url text not null, clock_out_at timestamptz,
 clock_out_lat numeric(10,7), clock_out_lon numeric(10,7), clock_out_photo_url text,
 status text not null default 'OPEN', created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_field_attendance_tenant_user ON field_attendance(tenant_id,user_id,clock_in_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_field_attendance_open_user ON field_attendance(tenant_id,user_id) WHERE status='OPEN';
CREATE TABLE IF NOT EXISTS field_job_events (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text references projects(id) on delete cascade, task_id text references tasks(id) on delete cascade,
 technician_id text not null references users(id), event_type text not null,
 occurred_at timestamptz not null default now(), latitude numeric(10,7), longitude numeric(10,7),
 photo_url text, customer_signature_url text, note text, created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_field_job_events_tenant_time ON field_job_events(tenant_id,occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_field_job_events_task ON field_job_events(tenant_id,task_id,occurred_at DESC);
ALTER TABLE field_attendance ENABLE ROW LEVEL SECURITY; ALTER TABLE field_attendance FORCE ROW LEVEL SECURITY;
ALTER TABLE field_job_events ENABLE ROW LEVEL SECURITY; ALTER TABLE field_job_events FORCE ROW LEVEL SECURITY;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='field_attendance' AND policyname='field_attendance_tenant') THEN CREATE POLICY field_attendance_tenant ON field_attendance USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='field_job_events' AND policyname='field_job_events_tenant') THEN CREATE POLICY field_job_events_tenant ON field_job_events USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
END $$;`)
	return err
}

func (d *Database) RecordFieldAttendance(ctx context.Context, tenantID, userID, action string, lat, lon float64, photoURL string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		if action == "CLOCK_IN" {
			if photoURL == "" || lat == 0 && lon == 0 {
				return fmt.Errorf("clock-in requires GPS coordinates and a photo")
			}
			_, err := tx.Exec(ctx, `INSERT INTO field_attendance(id,tenant_id,user_id,clock_in_at,clock_in_lat,clock_in_lon,clock_in_photo_url) VALUES($1,$2,$3,now(),$4,$5,$6)`, uuid.NewString(), tenantID, userID, lat, lon, photoURL)
			return err
		}
		if action != "CLOCK_OUT" {
			return fmt.Errorf("unsupported attendance action")
		}
		_, err := tx.Exec(ctx, `UPDATE field_attendance SET clock_out_at=now(),clock_out_lat=$1,clock_out_lon=$2,clock_out_photo_url=$3,status='CLOSED' WHERE tenant_id=$4 AND user_id=$5 AND status='OPEN'`, lat, lon, photoURL, tenantID, userID)
		return err
	})
}

func (d *Database) RecordFieldJobEvent(ctx context.Context, tenantID, userID, projectID, taskID, eventType string, lat, lon float64, photoURL, signatureURL, note string) error {
	allowed := map[string]bool{"ARRIVAL": true, "DEPARTURE": true, "CUSTOMER_SIGNED": true, "FIRST_TIME_FIX": true, "REVISIT_REQUIRED": true, "JOB_NOTE": true}
	if !allowed[eventType] {
		return fmt.Errorf("unsupported job event type")
	}
	if eventType == "CUSTOMER_SIGNED" && signatureURL == "" {
		return fmt.Errorf("customer signature event requires a signature reference")
	}
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO field_job_events(id,tenant_id,project_id,task_id,technician_id,event_type,latitude,longitude,photo_url,customer_signature_url,note) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,NULLIF($7,0),NULLIF($8,0),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''))`, uuid.NewString(), tenantID, projectID, taskID, userID, eventType, lat, lon, photoURL, signatureURL, note)
		return err
	})
}

func (d *Database) GetFieldTelemetry(ctx context.Context, tenantID string) (map[string]interface{}, error) {
	out := map[string]interface{}{"attendance": []map[string]interface{}{}, "job_events": []map[string]interface{}{}, "telemetry_summary": []map[string]interface{}{}, "job_summary": []map[string]interface{}{}}
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		q := func(sql string, args ...interface{}) ([]map[string]interface{}, error) {
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			cols := rows.FieldDescriptions()
			result := []map[string]interface{}{}
			for rows.Next() {
				vals := make([]interface{}, len(cols))
				ptrs := make([]interface{}, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return nil, err
				}
				m := map[string]interface{}{}
				for i, c := range cols {
					m[c.Name] = vals[i]
				}
				result = append(result, m)
			}
			return result, rows.Err()
		}
		var err error
		if out["attendance"], err = q(`SELECT a.id,a.user_id,u.name,u.region,a.clock_in_at,a.clock_in_lat,a.clock_in_lon,a.clock_out_at,a.status FROM field_attendance a JOIN users u ON u.id=a.user_id WHERE a.tenant_id=$1 ORDER BY a.clock_in_at DESC LIMIT 100`, tenantID); err != nil {
			return err
		}
		if out["job_events"], err = q(`SELECT e.id,e.project_id,e.task_id,e.technician_id,u.name AS technician_name,e.event_type,e.occurred_at,e.latitude,e.longitude,e.photo_url,e.customer_signature_url,e.note FROM field_job_events e JOIN users u ON u.id=e.technician_id WHERE e.tenant_id=$1 ORDER BY e.occurred_at DESC LIMIT 150`, tenantID); err != nil {
			return err
		}
		if out["telemetry_summary"], err = q(`SELECT COUNT(*) FILTER(WHERE status='OPEN') AS active_clockins,COUNT(*) FILTER(WHERE clock_in_at::date=CURRENT_DATE) AS clockins_today,COUNT(*) FILTER(WHERE status='CLOSED' AND clock_out_at::date=CURRENT_DATE) AS clockouts_today FROM field_attendance WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		if out["job_summary"], err = q(`SELECT COUNT(*) FILTER(WHERE occurred_at::date=CURRENT_DATE) AS events_today,COUNT(*) FILTER(WHERE event_type='FIRST_TIME_FIX' AND occurred_at::date=CURRENT_DATE) AS first_time_fix_today,COUNT(*) FILTER(WHERE event_type='REVISIT_REQUIRED' AND occurred_at::date=CURRENT_DATE) AS revisits_today,COUNT(*) FILTER(WHERE event_type='CUSTOMER_SIGNED' AND occurred_at::date=CURRENT_DATE) AS signatures_today FROM field_job_events WHERE tenant_id=$1`, tenantID); err != nil {
			return err
		}
		return nil
	})
	return out, err
}

func (d *Database) SeedIfNeeded() {
	ctx := context.Background()

	// Robust self-healing schema migration for existing databases
	upgradeSQL := `
	-- Transactional outbox: workflow execution is durably queued before commit.
	CREATE TABLE IF NOT EXISTS outbox_events (
		id text primary key, tenant_id text not null references tenants(id) on delete cascade,
		aggregate_type text not null, aggregate_id text not null, subject text not null,
		payload jsonb not null default '{}'::jsonb, headers jsonb not null default '{}'::jsonb,
		status text not null default 'PENDING', attempts integer not null default 0,
		next_attempt_at timestamptz not null default now(), published_at timestamptz,
		last_error text, created_at timestamptz not null default now()
	);
	CREATE INDEX IF NOT EXISTS idx_outbox_pending ON outbox_events(status, next_attempt_at, created_at);
	CREATE INDEX IF NOT EXISTS idx_outbox_tenant ON outbox_events(tenant_id, created_at DESC);

	-- Inbox/idempotency ledger: consumers can safely replay JetStream deliveries.
	CREATE TABLE IF NOT EXISTS inbox_messages (
		message_id text primary key, consumer_name text not null, tenant_id text not null,
		subject text not null, status text not null default 'PROCESSING', attempts integer not null default 1,
		received_at timestamptz not null default now(), processed_at timestamptz, last_error text
	);
	CREATE INDEX IF NOT EXISTS idx_inbox_consumer_status ON inbox_messages(consumer_name,status,received_at);
	CREATE INDEX IF NOT EXISTS idx_inbox_tenant ON inbox_messages(tenant_id,received_at DESC);

	-- Formal governance workflow ledger
	CREATE TABLE IF NOT EXISTS workflow_instances (
		id text primary key, tenant_id text not null references tenants(id) on delete cascade,
		entity_type text not null, entity_id text not null, workflow_key text not null,
		step integer not null default 1, status text not null default 'REQUESTED',
		requested_by text not null references users(id), checked_by text, executed_by text, reconciled_by text,
		reason text, created_at timestamptz not null default now(), updated_at timestamptz not null default now()
	);
	CREATE INDEX IF NOT EXISTS idx_workflow_tenant_status ON workflow_instances(tenant_id,status);
	CREATE INDEX IF NOT EXISTS idx_workflow_entity ON workflow_instances(tenant_id,entity_type,entity_id);
	CREATE TABLE IF NOT EXISTS workflow_decisions (
		id text primary key, instance_id text not null references workflow_instances(id) on delete cascade,
		tenant_id text not null references tenants(id) on delete cascade, actor_id text not null references users(id),
		action text not null, reason text, created_at timestamptz not null default now()
	);
	CREATE INDEX IF NOT EXISTS idx_workflow_decisions_instance ON workflow_decisions(instance_id);

	-- Ensure inventory_reservations table exists
	CREATE TABLE IF NOT EXISTS inventory_reservations (
		id text primary key,
		tenant_id text not null references tenants(id) on delete cascade,
		request_id text not null references material_requests(id) on delete cascade,
		item_id text not null references inventory_items(id) on delete restrict,
		reserved_for text not null references users(id),
		status text not null default 'RESERVED',
		created_at timestamptz not null default now(),
		unique (tenant_id, request_id),
		unique (tenant_id, item_id, status)
	);
	CREATE INDEX IF NOT EXISTS idx_inventory_reservations_tenant on inventory_reservations(tenant_id, status);
	ALTER TABLE inventory_reservations ENABLE ROW LEVEL SECURITY;
	ALTER TABLE inventory_reservations FORCE ROW LEVEL SECURITY;
	DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='inventory_reservations' AND policyname='inventory_reservation_tenant_isolation') THEN
			CREATE POLICY inventory_reservation_tenant_isolation ON inventory_reservations USING (tenant_id = current_setting('app.current_tenant_id', true));
		END IF;
	END $$;

	-- Ensure manager_id column exists in users
	ALTER TABLE users ADD COLUMN IF NOT EXISTS manager_id text;
	ALTER TABLE inventory_items ADD COLUMN IF NOT EXISTS unit_cost numeric(14,2) NOT NULL DEFAULT 0;
	ALTER TABLE payments ADD COLUMN IF NOT EXISTS workflow_id text;
	ALTER TABLE inbox_messages ADD COLUMN IF NOT EXISTS locked_at timestamptz;
	ALTER TABLE inbox_messages ADD COLUMN IF NOT EXISTS completed_at timestamptz;

	-- Business ledger / profitability tables for existing databases
	CREATE TABLE IF NOT EXISTS business_ledger_entries (
		id text primary key, tenant_id text not null references tenants(id) on delete cascade,
		transaction_id text not null, task_id text, invoice_id text, entity_type text not null, entity_id text not null,
		account text not null, entry_type text not null, amount numeric(14,2) not null, quantity numeric(14,4) not null default 1,
		unit_cost numeric(14,2) not null default 0, currency char(3) not null default 'KES', reference text, actor_id text,
		reversal_of text, created_at timestamptz not null default now(),
		unique (tenant_id, transaction_id, entry_type, account)
	);
	CREATE INDEX IF NOT EXISTS idx_ledger_tenant_task ON business_ledger_entries(tenant_id,task_id,created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_ledger_tenant_invoice ON business_ledger_entries(tenant_id,invoice_id,created_at DESC);
	ALTER TABLE business_ledger_entries ENABLE ROW LEVEL SECURITY; ALTER TABLE business_ledger_entries FORCE ROW LEVEL SECURITY;
	DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='business_ledger_entries' AND policyname='ledger_tenant_isolation') THEN
	CREATE POLICY ledger_tenant_isolation ON business_ledger_entries USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF; END $$;
	CREATE TABLE IF NOT EXISTS labor_rates (id text primary key, tenant_id text not null references tenants(id) on delete cascade, user_id text, role_name text, hourly_rate numeric(14,2) not null, currency char(3) not null default 'KES', active boolean not null default true, created_at timestamptz not null default now());
	CREATE INDEX IF NOT EXISTS idx_labor_rates_lookup ON labor_rates(tenant_id,user_id,role_name,active);
	ALTER TABLE labor_rates ENABLE ROW LEVEL SECURITY; ALTER TABLE labor_rates FORCE ROW LEVEL SECURITY;
	DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='labor_rates' AND policyname='labor_rates_tenant_isolation') THEN
	CREATE POLICY labor_rates_tenant_isolation ON labor_rates USING (tenant_id=current_setting('app.current_tenant_id',true)) WITH CHECK (tenant_id=current_setting('app.current_tenant_id',true)); END IF; END $$;

	-- Ensure invitations table exists
	CREATE TABLE IF NOT EXISTS invitations (
		id         text primary key,
		tenant_id  text not null references tenants(id) on delete cascade,
		email      text not null,
		name       text not null,
		role_name  text not null,
		region     text not null,
		manager_id text,
		token      text not null unique,
		status     text not null default 'Pending',
		created_at timestamptz not null default now()
	);

	-- Ensure indexes and RLS for invitations exist
	CREATE INDEX IF NOT EXISTS idx_invitations_tenant ON invitations(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_invitations_token ON invitations(token);
	ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
	ALTER TABLE invitations FORCE ROW LEVEL SECURITY;

	DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_policies WHERE tablename = 'invitations' AND policyname = 'tenant_isolation'
		) THEN
			CREATE POLICY tenant_isolation ON invitations USING (tenant_id = current_setting('app.current_tenant_id', true));
		END IF;
	END
	$$;

	-- Ensure support_tickets table exists
	CREATE TABLE IF NOT EXISTS support_tickets (
		id          text primary key,
		tenant_id   text not null references tenants(id) on delete cascade,
		customer_id text not null references customers(id) on delete cascade,
		title       text not null,
		description text,
		status      text not null default 'Open',
		task_id     text,
		created_at  timestamptz not null default now()
	);

	-- Ensure indexes and RLS for support_tickets exist
	CREATE INDEX IF NOT EXISTS idx_support_tickets_tenant ON support_tickets(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_support_tickets_customer ON support_tickets(customer_id);
	ALTER TABLE support_tickets ENABLE ROW LEVEL SECURITY;
	ALTER TABLE support_tickets FORCE ROW LEVEL SECURITY;

	DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_policies WHERE tablename = 'support_tickets' AND policyname = 'tenant_isolation'
		) THEN
			CREATE POLICY tenant_isolation ON support_tickets USING (tenant_id = current_setting('app.current_tenant_id', true));
		END IF;
	END
	$$;

	-- Ensure manager_id foreign key constraint is present on users
	DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint WHERE conname = 'users_manager_id_fkey'
		) THEN
			ALTER TABLE users ADD CONSTRAINT users_manager_id_fkey FOREIGN KEY (manager_id) REFERENCES users(id) ON DELETE SET NULL;
		END IF;
	END
	$$;

	-- Ensure procurement_orders have the confirmation columns
	ALTER TABLE procurement_orders ADD COLUMN IF NOT EXISTS barcode_photo_url text;
	ALTER TABLE procurement_orders ADD COLUMN IF NOT EXISTS confirmed_at timestamptz;
	ALTER TABLE procurement_orders ADD COLUMN IF NOT EXISTS confirmed_by text;

	-- Ensure invoice_notes table exists
	CREATE TABLE IF NOT EXISTS invoice_notes (
		id           text primary key,
		tenant_id    text not null references tenants(id) on delete cascade,
		request_id   text not null references material_requests(id) on delete cascade,
		item_name    text not null,
		allocated_sn text not null,
		task_id      text,
		requester_id text not null references users(id),
		usage_type   text not null default 'Internal',
		status       text not null default 'Paid_Usage_Support',
		invoice_id   text references invoices(id) on delete set null,
		payment_id   text references payments(id) on delete set null,
		created_at   timestamptz not null default now()
	);

	CREATE INDEX IF NOT EXISTS idx_invoice_notes_tenant ON invoice_notes(tenant_id);
	ALTER TABLE invoice_notes ENABLE ROW LEVEL SECURITY;
	ALTER TABLE invoice_notes FORCE ROW LEVEL SECURITY;

	DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_policies WHERE tablename = 'invoice_notes' AND policyname = 'tenant_isolation'
		) THEN
			CREATE POLICY tenant_isolation ON invoice_notes USING (tenant_id = current_setting('app.current_tenant_id', true));
		END IF;
	END
	$$;
	`
	_, _ = d.Pool.Exec(ctx, upgradeSQL)

	var count int
	err := d.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM tenants").Scan(&count)
	if err != nil {
		log.Println("Database schema is missing or tenants table does not exist. Initializing database schema...")
		_, execErr := d.Pool.Exec(ctx, SchemaSQL)
		if execErr != nil {
			log.Fatalf("failed to initialize database schema: %v", execErr)
		}
		log.Println("Database schema initialized successfully.")
	}
	if err := d.ensureCustomerProjectLifecycle(ctx); err != nil {
		log.Fatalf("failed to initialize customer/project lifecycle schema: %v", err)
	}
	if err := d.ensureFieldTelemetry(ctx); err != nil {
		log.Printf("failed to ensure field telemetry schema: %v", err)
	}

	if err := d.ensureProjectExecutionControl(ctx); err != nil {
		log.Fatalf("failed to initialize project execution control schema: %v", err)
	}
	if count > 0 {
		return
	}

	log.Println("Database is empty. Seeding initial multi-tenant default data...")

	d.CreateTenant("tenant_safari", "Safaricom ISP Services")
	d.CreateTenant("tenant_pioneer", "Pioneer Printer Maintenance")

	safariRoles := []struct {
		Name        string
		ParentRole  string
		Permissions []string
	}{
		{"field_technician", "", []string{"inventory:read", "tasks:read", "timesheets:submit"}},
		{"finance_officer", "", []string{"finance:read", "finance:write"}},
		{"manager", "field_technician", []string{"finance:read", "tasks:create", "tasks:approve", "timesheets:approve"}},
		{"tenant_admin", "manager", []string{"inventory:*", "finance:*", "tasks:*", "users:*"}},
	}
	for _, r := range safariRoles {
		err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO roles (tenant_id, name, parent_role, permissions) VALUES ($1, $2, NULLIF($3, ''), $4) ON CONFLICT DO NOTHING",
				"tenant_safari", r.Name, r.ParentRole, r.Permissions)
			return err
		})
		if err != nil {
			log.Fatalf("failed to seed roles for tenant_safari: %v", err)
		}
	}

	for _, r := range safariRoles {
		err = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO roles (tenant_id, name, parent_role, permissions) VALUES ($1, $2, NULLIF($3, ''), $4) ON CONFLICT DO NOTHING",
				"tenant_pioneer", r.Name, r.ParentRole, r.Permissions)
			return err
		})
		if err != nil {
			log.Fatalf("failed to seed roles for tenant_pioneer: %v", err)
		}
	}

	seedHash := mustHashSeedPassword("ChangeMe123!")

	if err = d.CreateUser("usr_safari_admin", "tenant_safari", "Alice Admin", "alice@safari.test", "tenant_admin", "Nairobi", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_safari_admin: %v", err)
	}
	if err = d.CreateUser("usr_safari_mgr", "tenant_safari", "Bob Manager", "bob@safari.test", "manager", "Nairobi", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_safari_mgr: %v", err)
	}
	if err = d.CreateUser("usr_safari_tech", "tenant_safari", "Charlie Tech", "charlie@safari.test", "field_technician", "Mombasa", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_safari_tech: %v", err)
	}

	if err = d.CreateUser("usr_pioneer_mgr", "tenant_pioneer", "Daniel Manager", "daniel@pioneer.test", "manager", "Kisumu", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_pioneer_mgr: %v", err)
	}
	if err = d.CreateUser("usr_pioneer_fin", "tenant_pioneer", "Eva Finance", "eva@pioneer.test", "finance_officer", "Kisumu", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_pioneer_fin: %v", err)
	}
	if err = d.CreateUser("usr_pioneer_tech", "tenant_pioneer", "Frank Tech", "frank@pioneer.test", "field_technician", "Nairobi", seedHash); err != nil {
		log.Fatalf("failed to seed user usr_pioneer_tech: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO customers (id, tenant_id, name, phone, email, dispatch_status) VALUES ('cust_saf_77', 'tenant_safari', 'Safaricom client Nairobi', '254711223344', 'saf_client@gmail.com', 'Pending') ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed customers for tenant_safari: %v", err)
	}

	err = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO customers (id, tenant_id, name, phone, email, dispatch_status) VALUES ('cust_pio_88', 'tenant_pioneer', 'Pioneer Kisumu printer client', '254755667788', 'pioneer_client@gmail.com', 'Pending') ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed customers for tenant_pioneer: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO material_requests (id, tenant_id, requester_id, item_name, status, created_at) VALUES ('req_safari_1', 'tenant_safari', 'usr_safari_tech', 'Huawei GPON ONU', 'Pending_Leader_Approval', NOW()) ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed material requests for tenant_safari: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_onu_1', 'tenant_safari', 'Huawei GPON ONU', 'SN-HUA-9901', 'In_Stock', NULL, 'Nairobi', 1) ON CONFLICT DO NOTHING")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_onu_2', 'tenant_safari', 'Huawei GPON ONU', 'SN-HUA-9902', 'Assigned', 'usr_safari_tech', 'Mombasa', 1) ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed inventory_items for tenant_safari: %v", err)
	}

	err = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_printer_part', 'tenant_pioneer', 'LaserJet Fuser Assembly', 'SN-HP-3030', 'In_Stock', NULL, 'Kisumu', 1) ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed inventory_items for tenant_pioneer: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region) VALUES ('inv_safari_1', 'tenant_safari', 'cust_saf_77', 5000.0, 2000.0, 3000.0, 'Partially_Paid', 'Nairobi') ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed invoices for tenant_safari: %v", err)
	}

	err = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region) VALUES ('inv_pioneer_1', 'tenant_pioneer', 'cust_pio_88', 15000.0, 0.0, 15000.0, 'Approved', 'Kisumu') ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed invoices for tenant_pioneer: %v", err)
	}

	if err = d.UpdateCustomerDeviceTransaction("cust_saf_77", "item_onu_2", "inv_safari_1", "Pending"); err != nil {
		log.Fatalf("failed to update customer device for tenant_safari: %v", err)
	}
	if err = d.UpdateCustomerDeviceTransaction("cust_pio_88", "item_printer_part", "inv_pioneer_1", "Pending"); err != nil {
		log.Fatalf("failed to update customer device for tenant_pioneer: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date) VALUES ('task_safari_install', 'tenant_safari', 'Fibre Home Installation', 'usr_safari_tech', 'usr_safari_mgr', 'In_Progress', 'Mombasa', CURRENT_DATE + 5) ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed tasks for tenant_safari: %v", err)
	}

	err = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date) VALUES ('task_pioneer_repair', 'tenant_pioneer', 'Office Copier Repair', 'usr_pioneer_tech', 'usr_pioneer_mgr', 'Pending', 'Nairobi', CURRENT_DATE + 2) ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed tasks for tenant_pioneer: %v", err)
	}

	err = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO timesheets (id, tenant_id, task_id, user_id, hours, worked_on, status) VALUES ('tsh_safari_1', 'tenant_safari', 'task_safari_install', 'usr_safari_tech', 4.5, CURRENT_DATE, 'Submitted') ON CONFLICT DO NOTHING")
		return err
	})
	if err != nil {
		log.Fatalf("failed to seed timesheets for tenant_safari: %v", err)
	}
}

func (d *Database) CreateInvitation(inv *types.Invitation) error {
	ctx := context.Background()
	return d.withTx(ctx, inv.TenantID, func(tx pgx.Tx) error {
		var emailExists bool
		_ = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1))", strings.TrimSpace(inv.Email)).Scan(&emailExists)
		if emailExists {
			return errors.New("email already registered")
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO invitations (id, tenant_id, email, name, role_name, region, manager_id, token, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10)`,
			inv.ID, inv.TenantID, strings.ToLower(strings.TrimSpace(inv.Email)), inv.Name, inv.RoleName, inv.Region, inv.ManagerID, inv.Token, inv.Status, inv.CreatedAt)
		return err
	})
}

func (d *Database) GetInvitationByToken(token string) (*types.Invitation, error) {
	ctx := context.Background()
	var inv types.Invitation
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, email, name, role_name, region, COALESCE(manager_id, ''), token, status, created_at FROM invitations WHERE token = $1", token)
		return row.Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Name, &inv.RoleName, &inv.Region, &inv.ManagerID, &inv.Token, &inv.Status, &inv.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (d *Database) AcceptInvitation(token string, passwordHash string) (*types.User, error) {
	ctx := context.Background()
	var user types.User
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		var inv types.Invitation
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, email, name, role_name, region, COALESCE(manager_id, ''), token, status FROM invitations WHERE token = $1", token)
		err := row.Scan(&inv.ID, &inv.TenantID, &inv.Email, &inv.Name, &inv.RoleName, &inv.Region, &inv.ManagerID, &inv.Token, &inv.Status)
		if err != nil {
			return err
		}
		if inv.Status != "Pending" {
			return errors.New("invitation is not pending")
		}

		// Update invitation status
		_, err = tx.Exec(ctx, "UPDATE invitations SET status = 'Accepted' WHERE id = $1", inv.ID)
		if err != nil {
			return err
		}

		user.ID = "usr_" + uuid.NewString()[:12]
		user.TenantID = inv.TenantID
		user.Name = inv.Name
		user.Email = inv.Email
		user.RoleName = inv.RoleName
		user.Region = inv.Region
		user.PasswordHash = passwordHash
		user.ManagerID = inv.ManagerID

		_, err = tx.Exec(ctx, `
			INSERT INTO users (id, tenant_id, name, email, role_name, region, password_hash, manager_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))`,
			user.ID, user.TenantID, user.Name, user.Email, user.RoleName, user.Region, user.PasswordHash, user.ManagerID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (d *Database) UpdateUserManager(userID, managerID string) error {
	ctx := context.Background()
	return d.withTx(ctx, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE users SET manager_id = NULLIF($1, '') WHERE id = $2", managerID, userID)
		return err
	})
}

func (d *Database) UpdateUserRole(userID, roleName string) error {
	ctx := context.Background()
	return d.withTx(ctx, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE users SET role_name = $1 WHERE id = $2", roleName, userID)
		return err
	})
}

// CreateLeadTransaction creates a CRM lead inside the tenant transaction.
func (d *Database) CreateLeadTransaction(ctx context.Context, l *types.Lead) error {
	return d.withTx(ctx, l.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO leads(id,tenant_id,name,phone,email,source,status,owner_id) VALUES($1,$2,$3,$4,$5,$6,'New',NULLIF($7,''))`, l.ID, l.TenantID, l.Name, l.Phone, l.Email, l.Source, l.OwnerID)
		return err
	})
}

func (d *Database) ConvertLeadToCustomer(ctx context.Context, tenantID, leadID, actor string) (*types.Customer, error) {
	var customer types.Customer
	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var name, phone, email, status string
		if err := tx.QueryRow(ctx, `SELECT name,COALESCE(phone,''),COALESCE(email,''),status FROM leads WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, leadID, tenantID).Scan(&name, &phone, &email, &status); err != nil {
			return err
		}
		if status == "Converted" {
			return fmt.Errorf("lead is already converted")
		}
		customer.ID = "cust_" + uuid.NewString()[:8]
		customer.TenantID = tenantID
		customer.Name = name
		customer.Phone = phone
		customer.Email = email
		customer.DispatchStatus = "Pending"
		if _, err := tx.Exec(ctx, `INSERT INTO customers(id,tenant_id,name,phone,email,dispatch_status) VALUES($1,$2,$3,$4,$5,'Pending')`, customer.ID, tenantID, name, phone, email); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE leads SET status='Converted',owner_id=$1 WHERE id=$2 AND tenant_id=$3`, actor, leadID, tenantID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (d *Database) CreateQuoteTransaction(ctx context.Context, q *types.Quote) error {
	return d.withTx(ctx, q.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO quotes(id,tenant_id,customer_id,lead_id,title,amount,status,valid_until,created_by) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,'Draft',$7,$8)`, q.ID, q.TenantID, q.CustomerID, q.LeadID, q.Title, q.Amount, q.ValidUntil, q.CreatedBy)
		return err
	})
}

func (d *Database) ApproveQuoteTransaction(ctx context.Context, tenantID, quoteID, actor string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var createdBy, status string
		if err := tx.QueryRow(ctx, `SELECT created_by,status FROM quotes WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, quoteID, tenantID).Scan(&createdBy, &status); err != nil {
			return err
		}
		if createdBy == actor {
			return fmt.Errorf("segregation of duties: quote creator cannot approve own quote")
		}
		if status != "Pending_Approval" && status != "Draft" {
			return fmt.Errorf("quote is not awaiting approval")
		}
		_, err := tx.Exec(ctx, `UPDATE quotes SET status='Approved',approved_by=$1 WHERE id=$2 AND tenant_id=$3`, actor, quoteID, tenantID)
		return err
	})
}

func (d *Database) CreateProjectTransaction(ctx context.Context, p *types.Project) error {
	return d.withTx(ctx, p.TenantID, func(tx pgx.Tx) error {
		if p.QuoteID != "" {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM quotes WHERE id=$1 AND tenant_id=$2`, p.QuoteID, p.TenantID).Scan(&status); err != nil {
				return err
			}
			if status != "Approved" {
				return fmt.Errorf("quote must be approved before project creation")
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO projects(id,tenant_id,customer_id,quote_id,name,status,region,start_date,target_date,created_by) VALUES($1,$2,$3,NULLIF($4,''),$5,'Planned',$6,$7,$8,$9)`, p.ID, p.TenantID, p.CustomerID, p.QuoteID, p.Name, p.Region, p.StartDate, p.TargetDate, p.CreatedBy)
		return err
	})
}

func (d *Database) LinkTaskToProject(ctx context.Context, tenantID, projectID, taskID, customerID string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO project_tasks(project_id,task_id,tenant_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, projectID, taskID, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tasks SET project_id=$1,customer_id=$2 WHERE id=$3 AND tenant_id=$4`, projectID, customerID, taskID)
		return err
	})
}

func (d *Database) AddProjectEvidence(ctx context.Context, e *types.ProjectEvidence) error {
	return d.withTx(ctx, e.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO project_evidence(id,tenant_id,project_id,task_id,evidence_type,url,note,captured_by) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, e.ID, e.TenantID, e.ProjectID, e.TaskID, e.EvidenceType, e.URL, e.Note, e.CapturedBy)
		return err
	})
}

func (d *Database) AddCustomerSatisfaction(ctx context.Context, s *types.CustomerSatisfaction) error {
	return d.withTx(ctx, s.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO customer_satisfaction(id,tenant_id,customer_id,project_id,rating,comment) VALUES($1,$2,$3,NULLIF($4,''),$5,$6)`, s.ID, s.TenantID, s.CustomerID, s.ProjectID, s.Rating, s.Comment)
		return err
	})
}

func (d *Database) GetCustomerLifecycle(ctx context.Context, tenantID string) (map[string]interface{}, error) {
	out := map[string]interface{}{"leads": []types.Lead{}, "quotes": []types.Quote{}, "projects": []types.Project{}, "evidence": []types.ProjectEvidence{}, "satisfaction": []types.CustomerSatisfaction{}}
	var leads []types.Lead
	var quotes []types.Quote
	var projects []types.Project
	var evidence []types.ProjectEvidence
	var satisfaction []types.CustomerSatisfaction
	if err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,tenant_id,name,COALESCE(phone,''),COALESCE(email,''),source,status,COALESCE(owner_id,''),created_at FROM leads WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x types.Lead
			if err := rows.Scan(&x.ID, &x.TenantID, &x.Name, &x.Phone, &x.Email, &x.Source, &x.Status, &x.OwnerID, &x.CreatedAt); err != nil {
				return err
			}
			leads = append(leads, x)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,tenant_id,customer_id,COALESCE(lead_id,''),title,amount,status,valid_until,created_by,COALESCE(approved_by,''),created_at FROM quotes WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x types.Quote
			if err := rows.Scan(&x.ID, &x.TenantID, &x.CustomerID, &x.LeadID, &x.Title, &x.Amount, &x.Status, &x.ValidUntil, &x.CreatedBy, &x.ApprovedBy, &x.CreatedAt); err != nil {
				return err
			}
			quotes = append(quotes, x)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,tenant_id,customer_id,COALESCE(quote_id,''),name,status,region,start_date,target_date,created_by,created_at FROM projects WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x types.Project
			if err := rows.Scan(&x.ID, &x.TenantID, &x.CustomerID, &x.QuoteID, &x.Name, &x.Status, &x.Region, &x.StartDate, &x.TargetDate, &x.CreatedBy, &x.CreatedAt); err != nil {
				return err
			}
			projects = append(projects, x)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,tenant_id,project_id,COALESCE(task_id,''),evidence_type,url,COALESCE(note,''),captured_by,created_at FROM project_evidence WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x types.ProjectEvidence
			if err := rows.Scan(&x.ID, &x.TenantID, &x.ProjectID, &x.TaskID, &x.EvidenceType, &x.URL, &x.Note, &x.CapturedBy, &x.CreatedAt); err != nil {
				return err
			}
			evidence = append(evidence, x)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT id,tenant_id,customer_id,COALESCE(project_id,''),rating,COALESCE(comment,''),created_at FROM customer_satisfaction WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x types.CustomerSatisfaction
			if err := rows.Scan(&x.ID, &x.TenantID, &x.CustomerID, &x.ProjectID, &x.Rating, &x.Comment, &x.CreatedAt); err != nil {
				return err
			}
			satisfaction = append(satisfaction, x)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}
	out["leads"] = leads
	out["quotes"] = quotes
	out["projects"] = projects
	out["evidence"] = evidence
	out["satisfaction"] = satisfaction
	return out, nil
}

// ensureProjectExecutionControl adds the execution control plane used by v11.
// It deliberately extends the existing project/task model instead of creating a second job system.
func (d *Database) ensureProjectExecutionControl(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS project_assignments (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text not null references projects(id) on delete cascade,
 task_id text references tasks(id) on delete cascade,
 technician_id text not null references users(id),
 role text not null default 'Technician',
 status text not null default 'Assigned',
 assigned_by text not null references users(id),
 assigned_at timestamptz not null default now(),
 UNIQUE(tenant_id, project_id, task_id, technician_id)
);
CREATE INDEX IF NOT EXISTS idx_project_assignments_tenant_project ON project_assignments(tenant_id,project_id,status);
CREATE TABLE IF NOT EXISTS project_schedule (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text not null references projects(id) on delete cascade,
 task_id text references tasks(id) on delete cascade,
 scheduled_start timestamptz not null, scheduled_end timestamptz,
 status text not null default 'Scheduled',
 notes text, created_by text not null references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_project_schedule_tenant_project ON project_schedule(tenant_id,project_id,scheduled_start);
CREATE TABLE IF NOT EXISTS project_bom (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text not null references projects(id) on delete cascade,
 task_id text references tasks(id) on delete set null,
 item_name text not null, quantity numeric(12,2) not null check(quantity > 0),
 unit_cost numeric(14,2) not null default 0, status text not null default 'Requested',
 requested_by text not null references users(id), created_at timestamptz not null default now()
);
CREATE INDEX IF NOT EXISTS idx_project_bom_tenant_project ON project_bom(tenant_id,project_id,status);
CREATE TABLE IF NOT EXISTS completion_reviews (
 id text primary key, tenant_id text not null references tenants(id) on delete cascade,
 project_id text not null references projects(id) on delete cascade,
 task_id text references tasks(id) on delete set null,
 status text not null default 'Submitted', evidence_count int not null default 0,
 submitted_by text not null references users(id), reviewed_by text references users(id),
 review_note text, submitted_at timestamptz not null default now(), reviewed_at timestamptz
);
DROP INDEX IF EXISTS uq_completion_review_open;
CREATE UNIQUE INDEX IF NOT EXISTS uq_completion_review_open ON completion_reviews(tenant_id,project_id,COALESCE(task_id,'')) WHERE status IN ('Submitted','Returned');
CREATE INDEX IF NOT EXISTS idx_completion_reviews_project ON completion_reviews(tenant_id,project_id,status);
ALTER TABLE project_assignments ENABLE ROW LEVEL SECURITY; ALTER TABLE project_assignments FORCE ROW LEVEL SECURITY;
ALTER TABLE project_schedule ENABLE ROW LEVEL SECURITY; ALTER TABLE project_schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE project_bom ENABLE ROW LEVEL SECURITY; ALTER TABLE project_bom FORCE ROW LEVEL SECURITY;
ALTER TABLE completion_reviews ENABLE ROW LEVEL SECURITY; ALTER TABLE completion_reviews FORCE ROW LEVEL SECURITY;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='project_assignments' AND policyname='tenant_isolation') THEN CREATE POLICY tenant_isolation ON project_assignments USING (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='project_schedule' AND policyname='tenant_isolation') THEN CREATE POLICY tenant_isolation ON project_schedule USING (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='project_bom' AND policyname='tenant_isolation') THEN CREATE POLICY tenant_isolation ON project_bom USING (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='completion_reviews' AND policyname='tenant_isolation') THEN CREATE POLICY tenant_isolation ON completion_reviews USING (tenant_id=current_setting('app.current_tenant_id',true)); END IF;
END $$;`)
	return err
}

func (d *Database) CreateProjectAssignment(ctx context.Context, tenantID, projectID, taskID, techID, role, actor string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND tenant_id=$2)`, projectID, tenantID).Scan(&ok); err != nil || !ok {
			return fmt.Errorf("project not found")
		}
		if taskID != "" {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND tenant_id=$2 AND project_id=$3)`, taskID, tenantID, projectID).Scan(&ok); err != nil || !ok {
				return fmt.Errorf("task is not linked to project")
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_assignments(id,tenant_id,project_id,task_id,technician_id,role,assigned_by) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7) ON CONFLICT (tenant_id,project_id,task_id,technician_id) DO UPDATE SET status='Assigned',role=EXCLUDED.role,assigned_by=EXCLUDED.assigned_by,assigned_at=now()`, uuid.NewString(), tenantID, projectID, taskID, techID, role, actor)
		return err
	})
}

func (d *Database) CreateProjectSchedule(ctx context.Context, tenantID, projectID, taskID string, start time.Time, end *time.Time, notes, actor string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND tenant_id=$2)`, projectID, tenantID).Scan(&exists); err != nil || !exists {
			return fmt.Errorf("project not found")
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_schedule(id,tenant_id,project_id,task_id,scheduled_start,scheduled_end,notes,created_by) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, uuid.NewString(), tenantID, projectID, taskID, start, end, notes, actor)
		return err
	})
}

func (d *Database) CreateProjectBOM(ctx context.Context, tenantID, projectID, taskID, item string, qty, unitCost float64, actor string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND tenant_id=$2)`, projectID, tenantID).Scan(&exists); err != nil || !exists {
			return fmt.Errorf("project not found")
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_bom(id,tenant_id,project_id,task_id,item_name,quantity,unit_cost,requested_by) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, uuid.NewString(), tenantID, projectID, taskID, item, qty, unitCost, actor)
		return err
	})
}

func (d *Database) SubmitCompletionReview(ctx context.Context, tenantID, projectID, taskID, actor string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var evidence int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM project_evidence WHERE tenant_id=$1 AND project_id=$2 AND ($3='' OR task_id=$3)`, tenantID, projectID, taskID).Scan(&evidence); err != nil {
			return err
		}
		if evidence == 0 {
			return fmt.Errorf("completion requires at least one evidence record")
		}
		var incomplete int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM project_tasks pt JOIN tasks t ON t.id=pt.task_id AND t.tenant_id=pt.tenant_id WHERE pt.tenant_id=$1 AND pt.project_id=$2 AND t.status <> 'Completed'`, tenantID, projectID).Scan(&incomplete); err != nil {
			return err
		}
		if incomplete > 0 {
			return fmt.Errorf("completion requires all linked tasks to be completed (%d remaining)", incomplete)
		}
		_, err := tx.Exec(ctx, `INSERT INTO completion_reviews(id,tenant_id,project_id,task_id,status,evidence_count,submitted_by) VALUES($1,$2,$3,NULLIF($4,''),'Submitted',$5,$6)`, uuid.NewString(), tenantID, projectID, taskID, evidence, actor)
		return err
	})
}

func (d *Database) ReviewCompletion(ctx context.Context, tenantID, reviewID, actor, status, note string) error {
	return d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		var submitted string
		if err := tx.QueryRow(ctx, `SELECT submitted_by FROM completion_reviews WHERE id=$1 AND tenant_id=$2 AND status='Submitted'`, reviewID, tenantID).Scan(&submitted); err != nil {
			return err
		}
		if submitted == actor {
			return fmt.Errorf("segregation of duties: submitter cannot review completion")
		}
		if status != "Approved" && status != "Returned" {
			return fmt.Errorf("review status must be Approved or Returned")
		}
		if _, err := tx.Exec(ctx, `UPDATE completion_reviews SET status=$1,reviewed_by=$2,review_note=$3,reviewed_at=now() WHERE id=$4 AND tenant_id=$5`, status, actor, note, reviewID, tenantID); err != nil {
			return err
		}
		if status == "Approved" {
			_, err := tx.Exec(ctx, `UPDATE projects SET status='Completed' WHERE id=(SELECT project_id FROM completion_reviews WHERE id=$1 AND tenant_id=$2) AND tenant_id=$2`, reviewID, tenantID)
			return err
		}
		return nil
	})
}

func (d *Database) GetProjectExecution(ctx context.Context, tenantID string) (map[string]interface{}, error) {
	out := map[string]interface{}{"assignments": []map[string]interface{}{}, "schedule": []map[string]interface{}{}, "bom": []map[string]interface{}{}, "reviews": []map[string]interface{}{}}
	q := func(sql string, args ...interface{}) ([]map[string]interface{}, error) {
		rows, err := d.Pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols := rows.FieldDescriptions()
		result := []map[string]interface{}{}
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			m := map[string]interface{}{}
			for i, c := range cols {
				m[c.Name] = vals[i]
			}
			result = append(result, m)
		}
		return result, rows.Err()
	}
	var err error
	if out["assignments"], err = q(`SELECT id,project_id,COALESCE(task_id,''),technician_id,role,status,assigned_at FROM project_assignments WHERE tenant_id=$1 ORDER BY assigned_at DESC LIMIT 200`, tenantID); err != nil {
		return nil, err
	}
	if out["schedule"], err = q(`SELECT id,project_id,COALESCE(task_id,''),scheduled_start,scheduled_end,status,COALESCE(notes,'') FROM project_schedule WHERE tenant_id=$1 ORDER BY scheduled_start DESC LIMIT 200`, tenantID); err != nil {
		return nil, err
	}
	if out["bom"], err = q(`SELECT id,project_id,COALESCE(task_id,''),item_name,quantity,unit_cost,status,created_at FROM project_bom WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 300`, tenantID); err != nil {
		return nil, err
	}
	if out["reviews"], err = q(`SELECT id,project_id,COALESCE(task_id,''),status,evidence_count,submitted_by,COALESCE(reviewed_by,''),COALESCE(review_note,''),submitted_at,reviewed_at FROM completion_reviews WHERE tenant_id=$1 ORDER BY submitted_at DESC LIMIT 200`, tenantID); err != nil {
		return nil, err
	}
	return out, nil
}

// GetFieldServiceIntelligence derives operational KPIs from the existing task,
// timesheet, ledger, inventory and customer-voice records. It deliberately does
// not invent attendance/route data that the ERP does not yet persist.
func (d *Database) GetFieldServiceIntelligence(ctx context.Context, tenantID string) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"summary":           map[string]interface{}{},
		"technicians":       []map[string]interface{}{},
		"regions":           []map[string]interface{}{},
		"material_variance": []map[string]interface{}{},
		"customer_voice":    []map[string]interface{}{},
	}
	q := func(sql string, args ...interface{}) ([]map[string]interface{}, error) {
		rows, err := d.Pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols := rows.FieldDescriptions()
		result := make([]map[string]interface{}, 0)
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			m := make(map[string]interface{}, len(cols))
			for i, c := range cols {
				m[c.Name] = vals[i]
			}
			result = append(result, m)
		}
		return result, rows.Err()
	}

	rows, err := q(`
		SELECT
			COUNT(*) FILTER (WHERE status <> 'Completed') AS open_tasks,
			COUNT(*) FILTER (WHERE status <> 'Completed' AND due_date < CURRENT_DATE) AS overdue_tasks,
			COUNT(*) FILTER (WHERE status <> 'Completed' AND due_date BETWEEN CURRENT_DATE AND CURRENT_DATE + 1) AS sla_risk,
			COUNT(*) FILTER (WHERE status = 'Completed') AS completed_tasks,
			COUNT(*) AS total_tasks,
			COUNT(DISTINCT assigned_to) FILTER (WHERE assigned_to IS NOT NULL AND assigned_to <> '' AND status <> 'Completed') AS active_technicians
		FROM tasks WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		out["summary"] = rows[0]
	}

	rows, err = q(`
		SELECT
			COALESCE(SUM(amount) FILTER (WHERE entry_type IN ('CREDIT','REVENUE')),0) AS revenue,
			COALESCE(SUM(amount) FILTER (WHERE account='Material Cost' OR account='Materials'),0) AS material_cost,
			COALESCE(SUM(amount) FILTER (WHERE account='Labor Cost' OR account='Labour Cost'),0) AS labor_cost,
			COALESCE(SUM(amount) FILTER (WHERE entry_type IN ('CREDIT','REVENUE')),0)
			- COALESCE(SUM(amount) FILTER (WHERE account IN ('Material Cost','Materials','Labor Cost','Labour Cost')),0) AS gross_margin
		FROM business_ledger_entries WHERE tenant_id=$1 AND reversal_of IS NULL`, tenantID)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		m := out["summary"].(map[string]interface{})
		for k, v := range rows[0] {
			m[k] = v
		}
	}

	rows, err = q(`SELECT COALESCE(AVG(rating),0) AS avg_csat, COUNT(*) AS responses FROM customer_satisfaction WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		m := out["summary"].(map[string]interface{})
		for k, v := range rows[0] {
			m[k] = v
		}
	}

	if out["technicians"], err = q(`
		SELECT u.id AS technician_id, u.name, u.region,
			COUNT(t.id) FILTER (WHERE t.status <> 'Completed') AS open_tasks,
			COUNT(t.id) FILTER (WHERE t.status <> 'Completed' AND t.due_date < CURRENT_DATE) AS overdue_tasks,
			COALESCE(SUM(ts.hours) FILTER (WHERE ts.status='Approved'),0) AS approved_hours
		FROM users u
		LEFT JOIN tasks t ON t.tenant_id=u.tenant_id AND t.assigned_to=u.id
		LEFT JOIN timesheets ts ON ts.tenant_id=u.tenant_id AND ts.user_id=u.id
		WHERE u.tenant_id=$1 AND LOWER(u.role_name) IN ('technician','field_technician','admin','manager')
		GROUP BY u.id,u.name,u.region ORDER BY open_tasks DESC, overdue_tasks DESC LIMIT 100`, tenantID); err != nil {
		return nil, err
	}

	if out["regions"], err = q(`
		SELECT region,
			COUNT(*) AS total_tasks,
			COUNT(*) FILTER (WHERE status='Completed') AS completed_tasks,
			COUNT(*) FILTER (WHERE status<>'Completed' AND due_date<CURRENT_DATE) AS overdue_tasks,
			ROUND(100.0*COUNT(*) FILTER (WHERE status='Completed')/NULLIF(COUNT(*),0),1) AS completion_pct
		FROM tasks WHERE tenant_id=$1 GROUP BY region ORDER BY overdue_tasks DESC, total_tasks DESC`, tenantID); err != nil {
		return nil, err
	}

	if out["material_variance"], err = q(`
		SELECT p.id AS project_id, p.name,
			COALESCE(SUM(b.quantity*b.unit_cost),0) AS planned_material_cost,
			COALESCE(SUM(l.amount) FILTER (WHERE l.account IN ('Material Cost','Materials')),0) AS actual_material_cost,
			COALESCE(SUM(l.amount) FILTER (WHERE l.account IN ('Material Cost','Materials')),0)-COALESCE(SUM(b.quantity*b.unit_cost),0) AS variance
		FROM projects p
		LEFT JOIN project_bom b ON b.tenant_id=p.tenant_id AND b.project_id=p.id
		LEFT JOIN tasks t ON t.tenant_id=p.tenant_id AND t.project_id=p.id
		LEFT JOIN business_ledger_entries l ON l.tenant_id=p.tenant_id AND l.task_id=t.id AND l.reversal_of IS NULL
		WHERE p.tenant_id=$1 GROUP BY p.id,p.name ORDER BY variance DESC LIMIT 100`, tenantID); err != nil {
		return nil, err
	}

	if out["customer_voice"], err = q(`
		SELECT c.id AS customer_id,c.name,COALESCE(AVG(s.rating),0) AS avg_rating,COUNT(s.id) AS responses,
			MAX(s.created_at) AS last_response
		FROM customers c LEFT JOIN customer_satisfaction s ON s.tenant_id=c.tenant_id AND s.customer_id=c.id
		WHERE c.tenant_id=$1 GROUP BY c.id,c.name HAVING COUNT(s.id)>0 ORDER BY avg_rating ASC, responses DESC LIMIT 100`, tenantID); err != nil {
		return nil, err
	}

	return out, nil
}
