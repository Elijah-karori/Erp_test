package db

import (
	"context"
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
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, name, email, role_name, region, password_hash FROM users WHERE id = $1", userID)
		return row.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash)
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
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, name, email, role_name, region, password_hash FROM users WHERE LOWER(email) = LOWER($1)", strings.TrimSpace(email))
		return row.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash)
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
					procID, tenantID, itemName, time.Now().Add(10 * 24 * time.Hour))
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
	ctx := context.Background()
	_ = d.withTx(ctx, pmt.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO payments (id, tenant_id, invoice_id, amount, payment_method, reference, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO NOTHING`,
			pmt.ID, pmt.TenantID, pmt.InvoiceID, pmt.Amount, pmt.PaymentMethod, pmt.Reference, pmt.Date)
		return err
	})
}

func (d *Database) GetTask(id string) (*types.Task, error) {
	ctx := context.Background()
	var t types.Task
	var assignedTo, dependsOn *string
	var dueDate *time.Time
	err := d.withTx(ctx, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on FROM tasks WHERE id = $1", id)
		return row.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn)
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
			INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, NULLIF($9, ''))
			ON CONFLICT (id) DO UPDATE SET
				title = $3,
				assigned_to = NULLIF($4, ''),
				created_by = $5,
				status = $6,
				region = $7,
				due_date = $8,
				depends_on = NULLIF($9, '')`,
			task.ID, task.TenantID, task.Title, task.AssignedTo, task.CreatedBy, task.Status, task.Region, task.DueDate, task.DependsOn)
		return err
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
			INSERT INTO users (id, tenant_id, name, email, role_name, region, password_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
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

	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
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
			INSERT INTO users (id, tenant_id, name, email, role_name, region, password_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
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
			VALUES ($1, $2, NULLIF($3, ''), $4, $5, 'Pending_Leader_Approval', $6)`,
			id, tenantID, taskID, requesterID, itemName, time.Now())
		return err
	})
}

func (d *Database) ApproveMaterialRequestTransaction(id, tenantID, approverID string) (*types.MaterialRequest, *types.ProcurementOrder, error) {
	ctx := context.Background()
	var req types.MaterialRequest
	var procOrder *types.ProcurementOrder
	var requesterID string
	var itemName string
	var taskID *string

	err := d.withTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT task_id, requester_id, item_name FROM material_requests WHERE id = $1 AND tenant_id = $2", id, tenantID).
			Scan(&taskID, &requesterID, &itemName)
		if err != nil {
			return fmt.Errorf("material request %s not found", id)
		}

		var itemID, serialNumber, region string
		err = tx.QueryRow(ctx, "SELECT id, serial_number, region FROM inventory_items WHERE tenant_id = $1 AND name = $2 AND status = 'In_Stock' LIMIT 1", tenantID, itemName).
			Scan(&itemID, &serialNumber, &region)

		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE inventory_items SET status = 'Assigned', assigned_to = $1 WHERE id = $2", requesterID, itemID)
			if err != nil {
				return err
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO inventory_history (tenant_id, item_id, from_status, to_status, changed_by, changed_at)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				tenantID, itemID, "In_Stock", "Assigned", approverID, time.Now())
			if err != nil {
				return err
			}

			_, err = tx.Exec(ctx, "UPDATE material_requests SET status = 'Fulfilled', allocated_sn = $1 WHERE id = $2", serialNumber, id)
			if err != nil {
				return err
			}

			var tID string
			if taskID != nil {
				tID = *taskID
			}

			req = types.MaterialRequest{
				ID:          id,
				TenantID:    tenantID,
				TaskID:      tID,
				RequesterID: requesterID,
				ItemName:    itemName,
				Status:      "Fulfilled",
				AllocatedSN: serialNumber,
				Timestamp:   time.Now(),
			}

			_ = d.CheckReorderThresholdAndTriggerProcurement(ctx, tx, tenantID, itemName)

		} else {
			_, err = tx.Exec(ctx, "UPDATE material_requests SET status = 'Procuring' WHERE id = $1", id)
			if err != nil {
				return err
			}

			procID := "proc_" + id
			_, err = tx.Exec(ctx, `
				INSERT INTO procurement_orders (id, tenant_id, request_id, item_name, expected_time, status)
				VALUES ($1, $2, $3, $4, $5, 'Bidding')
				ON CONFLICT (id) DO UPDATE SET status = 'Bidding'`,
				procID, tenantID, id, itemName, time.Now().Add(10*24*time.Hour))
			if err != nil {
				return err
			}

			var tID string
			if taskID != nil {
				tID = *taskID
			}

			req = types.MaterialRequest{
				ID:          id,
				TenantID:    tenantID,
				TaskID:      tID,
				RequesterID: requesterID,
				ItemName:    itemName,
				Status:      "Procuring",
				Timestamp:   time.Now(),
			}

			procOrder = &types.ProcurementOrder{
				ID:           procID,
				TenantID:     tenantID,
				RequestID:    id,
				ItemName:     itemName,
				ExpectedTime: time.Now().Add(10 * 24 * time.Hour),
				Status:       "Bidding",
			}
		}

		return nil
	})

	if err != nil {
		return nil, nil, err
	}
	return &req, procOrder, nil
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

func (d *Database) GetStateForTenant(tenantID string) map[string]interface{} {
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
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, name, email, role_name, region, password_hash FROM users WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var u types.User
				if err := rows.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.RoleName, &u.Region, &u.PasswordHash); err == nil {
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
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on FROM tasks WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t types.Task
				var assignedTo, dependsOn *string
				var dueDate *time.Time
				if err := rows.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn); err == nil {
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
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, request_id, item_name, expected_time, status FROM procurement_orders WHERE tenant_id = $1", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p types.ProcurementOrder
				var requestID *string
				if err := rows.Scan(&p.ID, &p.TenantID, &requestID, &p.ItemName, &p.ExpectedTime, &p.Status); err == nil {
					if requestID != nil {
						p.RequestID = *requestID
					}
					mu.Lock()
					procurementOrders[p.ID] = &p
					mu.Unlock()
				}
			}
			return nil
		})
	})

	g.Go(func() error {
		return d.withTx(gCtx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(gCtx, "SELECT id, tenant_id, title, assigned_to, created_by, status, region, due_date, depends_on FROM tasks WHERE tenant_id = $1 AND due_date < CURRENT_DATE AND status NOT IN ('Completed', 'Approved')", tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t types.Task
				var assignedTo, dependsOn *string
				var dueDate *time.Time
				if err := rows.Scan(&t.ID, &t.TenantID, &t.Title, &assignedTo, &t.CreatedBy, &t.Status, &t.Region, &dueDate, &dependsOn); err == nil {
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

	if err := g.Wait(); err != nil {
		log.Printf("GetStateForTenant background query error: %v", err)
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
	}
}

func (d *Database) SeedIfNeeded() {
	ctx := context.Background()
	var count int
	err := d.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM tenants").Scan(&count)
	if err == nil && count > 0 {
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
		_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO roles (tenant_id, name, parent_role, permissions) VALUES ($1, $2, NULLIF($3, ''), $4) ON CONFLICT DO NOTHING",
				"tenant_safari", r.Name, r.ParentRole, r.Permissions)
			return err
		})
	}

	for _, r := range safariRoles {
		_ = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO roles (tenant_id, name, parent_role, permissions) VALUES ($1, $2, NULLIF($3, ''), $4) ON CONFLICT DO NOTHING",
				"tenant_pioneer", r.Name, r.ParentRole, r.Permissions)
			return err
		})
	}

	seedHash := mustHashSeedPassword("ChangeMe123!")

	_ = d.CreateUser("usr_safari_admin", "tenant_safari", "Alice Admin", "alice@safari.test", "tenant_admin", "Nairobi", seedHash)
	_ = d.CreateUser("usr_safari_mgr", "tenant_safari", "Bob Manager", "bob@safari.test", "manager", "Nairobi", seedHash)
	_ = d.CreateUser("usr_safari_tech", "tenant_safari", "Charlie Tech", "charlie@safari.test", "field_technician", "Mombasa", seedHash)

	_ = d.CreateUser("usr_pioneer_mgr", "tenant_pioneer", "Daniel Manager", "daniel@pioneer.test", "manager", "Kisumu", seedHash)
	_ = d.CreateUser("usr_pioneer_fin", "tenant_pioneer", "Eva Finance", "eva@pioneer.test", "finance_officer", "Kisumu", seedHash)
	_ = d.CreateUser("usr_pioneer_tech", "tenant_pioneer", "Frank Tech", "frank@pioneer.test", "field_technician", "Nairobi", seedHash)

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO customers (id, tenant_id, name, phone, email, dispatch_status) VALUES ('cust_saf_77', 'tenant_safari', 'Safaricom client Nairobi', '254711223344', 'saf_client@gmail.com', 'Pending') ON CONFLICT DO NOTHING")
		return err
	})
	_ = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO customers (id, tenant_id, name, phone, email, dispatch_status) VALUES ('cust_pio_88', 'tenant_pioneer', 'Pioneer Kisumu printer client', '254755667788', 'pioneer_client@gmail.com', 'Pending') ON CONFLICT DO NOTHING")
		return err
	})

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO material_requests (id, tenant_id, requester_id, item_name, status, created_at) VALUES ('req_safari_1', 'tenant_safari', 'usr_safari_tech', 'Huawei GPON ONU', 'Pending_Leader_Approval', NOW()) ON CONFLICT DO NOTHING")
		return err
	})

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_onu_1', 'tenant_safari', 'Huawei GPON ONU', 'SN-HUA-9901', 'In_Stock', NULL, 'Nairobi', 1) ON CONFLICT DO NOTHING")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_onu_2', 'tenant_safari', 'Huawei GPON ONU', 'SN-HUA-9902', 'Assigned', 'usr_safari_tech', 'Mombasa', 1) ON CONFLICT DO NOTHING")
		return err
	})
	_ = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO inventory_items (id, tenant_id, name, serial_number, status, assigned_to, region, reorder_threshold) VALUES ('item_printer_part', 'tenant_pioneer', 'LaserJet Fuser Assembly', 'SN-HP-3030', 'In_Stock', NULL, 'Kisumu', 1) ON CONFLICT DO NOTHING")
		return err
	})

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region) VALUES ('inv_safari_1', 'tenant_safari', 'cust_saf_77', 5000.0, 2000.0, 3000.0, 'Partially_Paid', 'Nairobi') ON CONFLICT DO NOTHING")
		return err
	})
	_ = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO invoices (id, tenant_id, customer_id, total_amount, paid_amount, balance_amount, status, region) VALUES ('inv_pioneer_1', 'tenant_pioneer', 'cust_pio_88', 15000.0, 0.0, 15000.0, 'Approved', 'Kisumu') ON CONFLICT DO NOTHING")
		return err
	})

	_ = d.UpdateCustomerDeviceTransaction("cust_saf_77", "item_onu_2", "inv_safari_1", "Pending")
	_ = d.UpdateCustomerDeviceTransaction("cust_pio_88", "item_printer_part", "inv_pioneer_1", "Pending")

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date) VALUES ('task_safari_install', 'tenant_safari', 'Fibre Home Installation', 'usr_safari_tech', 'usr_safari_mgr', 'In_Progress', 'Mombasa', CURRENT_DATE + 5) ON CONFLICT DO NOTHING")
		return err
	})
	_ = d.withTx(ctx, "tenant_pioneer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tasks (id, tenant_id, title, assigned_to, created_by, status, region, due_date) VALUES ('task_pioneer_repair', 'tenant_pioneer', 'Office Copier Repair', 'usr_pioneer_tech', 'usr_pioneer_mgr', 'Pending', 'Nairobi', CURRENT_DATE + 2) ON CONFLICT DO NOTHING")
		return err
	})

	_ = d.withTx(ctx, "tenant_safari", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO timesheets (id, tenant_id, task_id, user_id, hours, worked_on, status) VALUES ('tsh_safari_1', 'tenant_safari', 'task_safari_install', 'usr_safari_tech', 4.5, CURRENT_DATE, 'Submitted') ON CONFLICT DO NOTHING")
		return err
	})
}
