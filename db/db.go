package db

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"erp-event-bus/types"
)

// Database represents a safe in-memory database mock for multi-tenant ERP operations
type Database struct {
	mu         sync.RWMutex
	Tenants    map[string]*types.Tenant
	Users      map[string]*types.User
	Roles      map[string]*types.Role
	Inventory  map[string]*types.InventoryItem
	Invoices   map[string]*types.Invoice
	Payments   map[string]*types.Payment
	Tasks      map[string]*types.Task
	Timesheets map[string]*types.Timesheet
}

// NewDatabase initializes a new mock database with multi-tenant seed data
func NewDatabase() *Database {
	d := &Database{
		Tenants:    make(map[string]*types.Tenant),
		Users:      make(map[string]*types.User),
		Roles:      make(map[string]*types.Role),
		Inventory:  make(map[string]*types.InventoryItem),
		Invoices:   make(map[string]*types.Invoice),
		Payments:   make(map[string]*types.Payment),
		Tasks:      make(map[string]*types.Task),
		Timesheets: make(map[string]*types.Timesheet),
	}

	// 1. Seed Roles with Hierarchy and Permissions
	// TenantAdmin (inherits Manager) > Manager (inherits Technician) > Technician
	d.Roles["field_technician"] = &types.Role{
		Name:        "field_technician",
		ParentRole:  "",
		Permissions: []string{"inventory:read", "tasks:read", "timesheets:submit"},
	}
	d.Roles["finance_officer"] = &types.Role{
		Name:        "finance_officer",
		ParentRole:  "",
		Permissions: []string{"finance:read", "finance:write"},
	}
	d.Roles["manager"] = &types.Role{
		Name:        "manager",
		ParentRole:  "field_technician",
		Permissions: []string{"finance:read", "tasks:create", "tasks:approve", "timesheets:approve"},
	}
	d.Roles["tenant_admin"] = &types.Role{
		Name:        "tenant_admin",
		ParentRole:  "manager",
		Permissions: []string{"inventory:*", "finance:*", "tasks:*", "users:*"},
	}

	// 2. Seed Tenants (Representing different enterprise subscribers)
	d.Tenants["tenant_safari"] = &types.Tenant{ID: "tenant_safari", Name: "Safaricom ISP Services"}
	d.Tenants["tenant_pioneer"] = &types.Tenant{ID: "tenant_pioneer", Name: "Pioneer Printer Maintenance"}

	// 3. Seed Users across Tenants and Regions
	// Tenant: Safari
	d.Users["usr_safari_admin"] = &types.User{ID: "usr_safari_admin", TenantID: "tenant_safari", Name: "Alice Admin", RoleName: "tenant_admin", Region: "Nairobi"}
	d.Users["usr_safari_mgr"] = &types.User{ID: "usr_safari_mgr", TenantID: "tenant_safari", Name: "Bob Manager", RoleName: "manager", Region: "Nairobi"}
	d.Users["usr_safari_tech"] = &types.User{ID: "usr_safari_tech", TenantID: "tenant_safari", Name: "Charlie Tech", RoleName: "field_technician", Region: "Mombasa"}

	// Tenant: Pioneer
	d.Users["usr_pioneer_mgr"] = &types.User{ID: "usr_pioneer_mgr", TenantID: "tenant_pioneer", Name: "Daniel Manager", RoleName: "manager", Region: "Kisumu"}
	d.Users["usr_pioneer_fin"] = &types.User{ID: "usr_pioneer_fin", TenantID: "tenant_pioneer", Name: "Eva Finance", RoleName: "finance_officer", Region: "Kisumu"}
	d.Users["usr_pioneer_tech"] = &types.User{ID: "usr_pioneer_tech", TenantID: "tenant_pioneer", Name: "Frank Tech", RoleName: "field_technician", Region: "Nairobi"}

	// 4. Seed Serialized Inventory
	d.Inventory["item_onu_1"] = &types.InventoryItem{
		ID: "item_onu_1", TenantID: "tenant_safari", Name: "Huawei GPON ONU", SerialNumber: "SN-HUA-9901", Status: "In_Stock", Region: "Nairobi",
	}
	d.Inventory["item_onu_2"] = &types.InventoryItem{
		ID: "item_onu_2", TenantID: "tenant_safari", Name: "Huawei GPON ONU", SerialNumber: "SN-HUA-9902", Status: "Assigned", AssignedTo: "usr_safari_tech", Region: "Mombasa",
	}
	d.Inventory["item_printer_part"] = &types.InventoryItem{
		ID: "item_printer_part", TenantID: "tenant_pioneer", Name: "LaserJet Fuser Assembly", SerialNumber: "SN-HP-3030", Status: "In_Stock", Region: "Kisumu",
	}

	// 5. Seed Invoices (Simulating partial payments and balance tracking)
	d.Invoices["inv_safari_1"] = &types.Invoice{
		ID: "inv_safari_1", TenantID: "tenant_safari", CustomerID: "cust_saf_77", TotalAmount: 5000.0, PaidAmount: 2000.0, BalanceAmount: 3000.0, Status: "Partially_Paid", Region: "Nairobi",
	}
	d.Invoices["inv_pioneer_1"] = &types.Invoice{
		ID: "inv_pioneer_1", TenantID: "tenant_pioneer", CustomerID: "cust_pio_88", TotalAmount: 15000.0, PaidAmount: 0.0, BalanceAmount: 15000.0, Status: "Approved", Region: "Kisumu",
	}

	// 6. Seed Tasks & Timesheets
	d.Tasks["task_safari_install"] = &types.Task{
		ID: "task_safari_install", TenantID: "tenant_safari", Title: "Fibre Home Installation", AssignedTo: "usr_safari_tech", CreatedBy: "usr_safari_mgr", Status: "In_Progress", Region: "Mombasa",
	}
	d.Tasks["task_pioneer_repair"] = &types.Task{
		ID: "task_pioneer_repair", TenantID: "tenant_pioneer", Title: "Office Copier Repair", AssignedTo: "usr_pioneer_tech", CreatedBy: "usr_pioneer_mgr", Status: "Pending", Region: "Nairobi",
	}

	d.Timesheets["tsh_safari_1"] = &types.Timesheet{
		ID: "tsh_safari_1", TenantID: "tenant_safari", TaskID: "task_safari_install", UserID: "usr_safari_tech", Hours: 4.5, Date: time.Now(), Status: "Submitted",
	}

	return d
}

// CheckPermission returns true if the given role name (or parent roles) carries the required permission
func (d *Database) CheckPermission(roleName string, requiredPermission string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	current := roleName
	visited := make(map[string]bool)

	for current != "" {
		if visited[current] {
			break // Guard cyclic structures
		}
		visited[current] = true

		role, exists := d.Roles[current]
		if !exists {
			break
		}

		for _, perm := range role.Permissions {
			if perm == requiredPermission || perm == "*" {
				return true
			}
			// Prefix support (e.g., "inventory:*" matches "inventory:read")
			if len(perm) > 2 && perm[len(perm)-2:] == ":*" {
				prefix := perm[:len(perm)-2]
				reqPrefix := requiredPermission
				if idx := len(requiredPermission); idx > 0 {
					// Extract prefix up to colon
					for i, char := range requiredPermission {
						if char == ':' {
							reqPrefix = requiredPermission[:i]
							break
						}
					}
				}
				if prefix == reqPrefix {
					return true
				}
			}
		}

		// Traverse up the parent role hierarchy
		current = role.ParentRole
	}

	return false
}

// UserExists checks if a user exists
func (d *Database) GetUser(userID string) (*types.User, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	u, exists := d.Users[userID]
	if !exists {
		return nil, errors.New("user not found")
	}
	return u, nil
}

// IsSubordinate returns true if managerRole inherited superiority over subordinateRole
func (d *Database) IsSubordinate(managerRole, subordinateRole string) bool {
	if managerRole == subordinateRole {
		return true
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	current := managerRole
	for current != "" {
		role, exists := d.Roles[current]
		if !exists {
			break
		}
		if role.ParentRole == subordinateRole {
			return true
		}
		current = role.ParentRole
	}
	return false
}

// SaveInventoryItem updates or inserts an inventory item
func (d *Database) SaveInventoryItem(item *types.InventoryItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Inventory[item.ID] = item
}

// GetInventoryItem retrieves inventory details
func (d *Database) GetInventoryItem(id string) (*types.InventoryItem, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	item, exists := d.Inventory[id]
	if !exists {
		return nil, fmt.Errorf("item %s not found", id)
	}
	return item, nil
}

// SaveInvoice updates or inserts invoice status
func (d *Database) SaveInvoice(invoice *types.Invoice) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Invoices[invoice.ID] = invoice
}

// GetInvoice retrieves invoice records
func (d *Database) GetInvoice(id string) (*types.Invoice, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	inv, exists := d.Invoices[id]
	if !exists {
		return nil, fmt.Errorf("invoice %s not found", id)
	}
	return inv, nil
}

// SavePayment inserts payment
func (d *Database) SavePayment(pmt *types.Payment) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Payments[pmt.ID] = pmt
}

// GetTask retrieves a task
func (d *Database) GetTask(id string) (*types.Task, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	t, exists := d.Tasks[id]
	if !exists {
		return nil, fmt.Errorf("task %s not found", id)
	}
	return t, nil
}

// SaveTask updates task state
func (d *Database) SaveTask(task *types.Task) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Tasks[task.ID] = task
}

// GetTimesheet retrieves a timesheet
func (d *Database) GetTimesheet(id string) (*types.Timesheet, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	t, exists := d.Timesheets[id]
	if !exists {
		return nil, fmt.Errorf("timesheet %s not found", id)
	}
	return t, nil
}

// SaveTimesheet updates timesheet state
func (d *Database) SaveTimesheet(ts *types.Timesheet) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Timesheets[ts.ID] = ts
}

// CreateTenant creates a new Tenant dynamically
func (d *Database) CreateTenant(id, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Tenants[id] = &types.Tenant{ID: id, Name: name}
}

// CreateUser creates a new User dynamically
func (d *Database) CreateUser(id, tenantID, name, roleName, region string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Users[id] = &types.User{ID: id, TenantID: tenantID, Name: name, RoleName: roleName, Region: region}
}

// UpdateRolePermissions allows interactive toggling of RBAC permissions from the UI console
func (d *Database) UpdateRolePermissions(roleName string, permissions []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, exists := d.Roles[roleName]; exists {
		r.Permissions = permissions
	}
}

// AllocateInventoryItemTransaction allocates a serialized device under safe write lock protection
func (d *Database) AllocateInventoryItemTransaction(id string, assignedTo string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	item, exists := d.Inventory[id]
	if !exists {
		return fmt.Errorf("item %s not found", id)
	}

	// Clone to avoid pointer data race under concurrent UI reads
	itemCopy := *item
	itemCopy.Status = "Assigned"
	itemCopy.AssignedTo = assignedTo
	d.Inventory[id] = &itemCopy
	return nil
}

// ApplyPaymentTransaction updates invoice status and outstanding balance under safe lock
func (d *Database) ApplyPaymentTransaction(id string, amount float64) (*types.Invoice, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inv, exists := d.Invoices[id]
	if !exists {
		return nil, fmt.Errorf("invoice %s not found", id)
	}

	invCopy := *inv
	invCopy.PaidAmount += amount
	invCopy.BalanceAmount = invCopy.TotalAmount - invCopy.PaidAmount
	if invCopy.BalanceAmount <= 0 {
		invCopy.Status = "Paid"
	} else {
		invCopy.Status = "Partially_Paid"
	}
	d.Invoices[id] = &invCopy
	return &invCopy, nil
}

// ApproveTimesheetTransaction approves technician hours under safe lock
func (d *Database) ApproveTimesheetTransaction(id string, approvedBy string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts, exists := d.Timesheets[id]
	if !exists {
		return fmt.Errorf("timesheet %s not found", id)
	}

	tsCopy := *ts
	tsCopy.Status = "Approved"
	tsCopy.ApprovedBy = approvedBy
	d.Timesheets[id] = &tsCopy
	return nil
}

// GetRoles returns a thread-safe deep copy of the roles map
func (d *Database) GetRoles() map[string][]string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rolesCopy := make(map[string][]string)
	for name, r := range d.Roles {
		permsCopy := make([]string, len(r.Permissions))
		copy(permsCopy, r.Permissions)
		rolesCopy[name] = permsCopy
	}
	return rolesCopy
}

// GetState returns thread-safe deep copy of ERP collections
func (d *Database) GetState() map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()

	inventoryCopy := make(map[string]*types.InventoryItem)
	for k, v := range d.Inventory {
		inventoryCopy[k] = v
	}

	invoicesCopy := make(map[string]*types.Invoice)
	for k, v := range d.Invoices {
		invoicesCopy[k] = v
	}

	tasksCopy := make(map[string]*types.Task)
	for k, v := range d.Tasks {
		tasksCopy[k] = v
	}

	timesheetsCopy := make(map[string]*types.Timesheet)
	for k, v := range d.Timesheets {
		timesheetsCopy[k] = v
	}

	return map[string]interface{}{
		"tenants":    d.Tenants,
		"users":      d.Users,
		"inventory":  inventoryCopy,
		"invoices":   invoicesCopy,
		"tasks":      tasksCopy,
		"timesheets": timesheetsCopy,
	}
}
