package handler

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"erp-event-bus/db"
)

type UIHandler struct {
	db       *db.Database
	sqliteDB *db.SQLiteDB
}

func NewUIHandler(database *db.Database, sdb *db.SQLiteDB) *UIHandler {
	return &UIHandler{db: database, sqliteDB: sdb}
}

type UpdateRBACPayload struct {
	RoleName    string   `json:"role_name"`
	Permissions []string `json:"permissions"`
}

type CreateTenantPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CreateUserPayload struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	RoleName string `json:"role_name"`
	Region   string `json:"region"`
}

// ServeDashboard returns the gorgeous fully-functional Tailwind UI
func (h *UIHandler) ServeDashboard(c echo.Context) error {
	return c.HTML(http.StatusOK, htmlContent)
}

// CreateTenantHandler creates a new tenant subscriber dynamically
func (h *UIHandler) CreateTenant(c echo.Context) error {
	var payload CreateTenantPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.ID == "" || payload.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Tenant ID and Name are required"})
	}

	h.db.CreateTenant(payload.ID, payload.Name)
	h.sqliteDB.Log(payload.ID, "SYSTEM", "CreateTenant_Success", fmt.Sprintf("Tenant subscriber '%s' registered dynamically", payload.Name))

	return c.JSON(http.StatusOK, map[string]string{"message": "Tenant registered successfully"})
}

// CreateUserHandler creates a new user dynamically
func (h *UIHandler) CreateUser(c echo.Context) error {
	var payload CreateUserPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if payload.ID == "" || payload.TenantID == "" || payload.Name == "" || payload.RoleName == "" || payload.Region == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "All user registration fields are required"})
	}

	h.db.CreateUser(payload.ID, payload.TenantID, payload.Name, payload.RoleName, payload.Region)
	h.sqliteDB.Log(payload.TenantID, "SYSTEM", "CreateUser_Success", fmt.Sprintf("User %s (%s) registered under tenant %s", payload.Name, payload.RoleName, payload.TenantID))

	return c.JSON(http.StatusOK, map[string]string{"message": "User registered successfully"})
}

// GetState returns the current in-memory DB state for UI rendering
func (h *UIHandler) GetState(c echo.Context) error {
	state := h.db.GetState()
	// Add roles definition to state so UI policy manager can see active permissions
	state["roles"] = h.db.GetRoles()
	return c.JSON(http.StatusOK, state)
}

// UpdateRBAC handles interactive RBAC/ABAC permission toggling from UI
func (h *UIHandler) UpdateRBAC(c echo.Context) error {
	var payload UpdateRBACPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	h.db.UpdateRolePermissions(payload.RoleName, payload.Permissions)

	tenantID := c.Request().Header.Get("Authorization-Tenant-Id")
	userID := c.Request().Header.Get("Authorization-User-Id")
	if tenantID == "" {
		tenantID = "tenant_safari"
	}
	if userID == "" {
		userID = "usr_safari_admin"
	}

	h.sqliteDB.Log(tenantID, userID, "UpdateRBAC_Success", fmt.Sprintf("Role '%s' permissions updated dynamically to %v", payload.RoleName, payload.Permissions))

	return c.JSON(http.StatusOK, map[string]string{"message": "Permissions updated successfully"})
}

// GetLogs returns live SQLite audit logs
func (h *UIHandler) GetLogs(c echo.Context) error {
	logs, err := h.sqliteDB.GetLogs()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, logs)
}

// ExportLogsExcel downloads the beautiful Excel audit sheet
func (h *UIHandler) ExportLogsExcel(c echo.Context) error {
	logs, err := h.sqliteDB.GetLogs()
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	buf, err := db.GenerateAuditExcel(logs)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	c.Response().Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=erp_audit_log.xlsx")
	return c.Blob(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf)
}

const htmlContent = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SME Kenya ERP Event Bus Console</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <link href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.0.0/css/all.min.css" rel="stylesheet">
    <script>
        tailwind.config = {
            theme: {
                extend: {
                    colors: {
                        brand: {
                            50: '#f5f7f6',
                            100: '#e1e7e4',
                            500: '#2f4f4f',
                            600: '#243e3e',
                            900: '#142222',
                        }
                    }
                }
            }
        }
    </script>
</head>
<body class="bg-brand-900 text-slate-100 min-h-screen font-sans overflow-x-hidden">

    <!-- LOGIN / AUTHENTICATION GATE SCREEN -->
    <div id="loginGate" class="min-h-screen flex items-center justify-center p-4 bg-brand-900">
        <div class="max-w-md w-full bg-brand-500 rounded-2xl border border-brand-600 p-6 md:p-8 shadow-2xl space-y-6">
            <div class="text-center space-y-2">
                <i class="fa-solid fa-network-wired text-emerald-400 text-5xl"></i>
                <h2 class="text-2xl font-bold tracking-wide text-white">SME Kenya ERP Login</h2>
                <p class="text-sm text-slate-300">Choose your workspace user profile or register below</p>
            </div>

            <div class="space-y-4">
                <div>
                    <label class="block text-xs font-bold text-slate-400 mb-1">Select Active Profile</label>
                    <select id="loginUserSelect" class="w-full bg-brand-900 border border-brand-600 rounded-lg px-4 py-3 text-sm text-slate-200 focus:outline-none focus:ring-2 focus:ring-emerald-400 focus:border-emerald-400">
                    </select>
                </div>

                <button onclick="handleLogin()" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white font-bold py-3 rounded-lg transition duration-200 shadow-md">
                    Enter Workspace
                </button>
            </div>

            <div class="border-t border-brand-600 pt-4 flex justify-between text-xs font-semibold text-emerald-400">
                <button onclick="openRegisterTenantModal()" class="hover:underline">Register New Tenant</button>
                <button onclick="openRegisterUserModal()" class="hover:underline">Create User Profile</button>
            </div>
        </div>
    </div>

    <!-- MAIN CO-LOCATED ERP DASHBOARD VIEW -->
    <div id="dashboardApp" class="hidden min-h-screen flex flex-col lg:flex-row">

        <!-- MOBILE NAVIGATION HEADER (Visible on mobile/tablet, hidden on desktop) -->
        <header class="lg:hidden bg-brand-500 border-b border-brand-600 px-4 py-4 flex items-center justify-between shadow-md sticky top-0 z-50">
            <div class="flex items-center space-x-3">
                <i class="fa-solid fa-network-wired text-emerald-400 text-xl"></i>
                <span id="mobileTenantTitle" class="text-md font-bold tracking-wide text-white truncate max-w-[150px]">SME Kenya ERP</span>
            </div>

            <div class="flex items-center space-x-3">
                <div class="relative bg-brand-900 p-2 rounded-lg border border-brand-600 cursor-pointer" onclick="toggleNotificationPane()">
                    <i class="fa-regular fa-bell text-slate-300 text-xs"></i>
                    <span id="mobileNotifBadge" class="absolute top-0 right-0 w-2 h-2 bg-rose-500 rounded-full border border-brand-900 hidden"></span>
                </div>
                <button onclick="toggleMobileSidebar()" class="bg-brand-900 border border-brand-600 text-slate-200 p-2 rounded-lg hover:bg-brand-600 focus:outline-none">
                    <i class="fa-solid fa-bars text-lg" id="hamburgerIcon"></i>
                </button>
            </div>
        </header>

        <!-- Role-Based Responsive Sidebar (Floating Drawer on mobile/tablet, pinned column on desktop) -->
        <aside id="sidebarDrawer" class="hidden lg:flex w-full lg:w-64 bg-brand-500 border-r border-brand-600 flex-col shadow-xl shrink-0 fixed lg:static inset-y-0 left-0 z-40 lg:z-auto transition-transform duration-300 transform lg:transform-none">

            <!-- Brand summary header (Hidden on mobile) -->
            <div class="hidden lg:flex px-6 py-5 border-b border-brand-600 items-center justify-between bg-brand-600">
                <div class="flex items-center space-x-3">
                    <i class="fa-solid fa-cubes text-emerald-400 text-2xl"></i>
                    <div>
                        <div id="sidebarTenantName" class="text-sm font-bold truncate max-w-[150px]">Safaricom ISP</div>
                        <div class="text-[10px] text-slate-400 tracking-widest uppercase">Ecosystem</div>
                    </div>
                </div>
            </div>

            <!-- Profile Summary Box -->
            <div class="p-4 mx-4 my-4 bg-brand-900 border border-brand-600 rounded-lg flex items-center space-x-3">
                <div class="w-8 h-8 rounded-full bg-brand-500 flex items-center justify-center text-emerald-300 font-bold" id="sidebarAvatar">
                    A
                </div>
                <div class="flex-1 min-w-0">
                    <div id="sidebarUserName" class="text-xs font-bold truncate text-white">Alice Admin</div>
                    <div id="sidebarRoleLabel" class="text-[9px] font-mono text-emerald-400 font-semibold truncate bg-brand-500 px-1.5 py-0.5 rounded inline-block mt-0.5">tenant_admin</div>
                </div>
            </div>

            <!-- Sidebar Navigation links -->
            <nav class="flex-1 px-4 space-y-1.5 text-sm font-medium">
                <button onclick="switchModuleView('dashboard')" id="navLink_dashboard" class="w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg bg-emerald-600 text-white font-bold transition">
                    <i class="fa-solid fa-chart-line w-5 text-center"></i>
                    <span>Dashboard Home</span>
                </button>
                <button onclick="switchModuleView('inventory')" id="navLink_inventory" class="w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition">
                    <i class="fa-solid fa-boxes-stacked w-5 text-center text-emerald-400"></i>
                    <span>Inventory Serial</span>
                </button>
                <button onclick="switchModuleView('finance')" id="navLink_finance" class="w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition">
                    <i class="fa-solid fa-file-invoice-dollar w-5 text-center text-amber-400"></i>
                    <span>Finance M-Pesa</span>
                </button>
                <button onclick="switchModuleView('tasks')" id="navLink_tasks" class="w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition">
                    <i class="fa-solid fa-list-check w-5 text-center text-sky-400"></i>
                    <span>Tasks &amp; Timesheets</span>
                </button>
                <button onclick="switchModuleView('rbac')" id="navLink_rbac" class="w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition">
                    <i class="fa-solid fa-shield-halved w-5 text-center text-rose-400"></i>
                    <span>RBAC Policy</span>
                </button>
            </nav>

            <!-- Bottom utility logout -->
            <div class="p-4 border-t border-brand-600">
                <button onclick="handleLogout()" class="w-full flex items-center justify-center space-x-2 bg-brand-900 border border-brand-600 hover:bg-rose-950 hover:text-white text-rose-400 font-bold py-2 rounded-lg transition duration-200 text-xs">
                    <i class="fa-solid fa-right-from-bracket"></i>
                    <span>Logout Account</span>
                </button>
            </div>
        </aside>

        <!-- Dynamic Module view layout panel -->
        <div class="flex-1 flex flex-col min-w-0 bg-brand-900 overflow-y-auto">

            <!-- Dashboard Top Action Headers (Visible only on desktop) -->
            <header class="hidden lg:flex bg-brand-500 border-b border-brand-600 px-8 py-4 items-center justify-between shadow-sm">
                <div>
                    <h2 id="currentModuleTitle" class="text-lg font-bold text-white">Dashboard Home</h2>
                    <p class="text-xs text-slate-400">Live Kenya-SME enterprise state</p>
                </div>
                <div class="flex items-center space-x-4">
                    <div class="relative bg-brand-900 p-2.5 rounded-lg border border-brand-600 cursor-pointer" onclick="toggleNotificationPane()">
                        <i class="fa-regular fa-bell text-slate-300 text-sm"></i>
                        <span id="notifBadge" class="absolute top-0 right-0 w-2.5 h-2.5 bg-rose-500 rounded-full border border-brand-900 hidden"></span>
                    </div>
                </div>
            </header>

            <!-- Notification Drawer Pane -->
            <div id="notifPane" class="hidden mx-4 lg:mx-8 mt-4 p-4 bg-brand-500 rounded-xl border border-brand-600 shadow-lg space-y-3">
                <h4 class="text-xs uppercase font-bold text-slate-400 flex items-center justify-between">
                    <span>Recent Broadcast Alerts</span>
                    <button onclick="clearNotifications()" class="text-[10px] text-rose-400 hover:underline">Clear All</button>
                </h4>
                <div id="notifList" class="space-y-2 max-h-[200px] overflow-y-auto font-mono text-[11px] text-emerald-300">
                    <div class="p-2 bg-brand-900 rounded border border-brand-600 italic text-slate-500">No recent notifications logged.</div>
                </div>
            </div>

            <!-- Workspace Panels -->
            <div class="p-4 md:p-8 max-w-6xl w-full mx-auto space-y-6">

                <!-- Module A: Dashboard View -->
                <div id="view_dashboard" class="space-y-6">
                    <!-- Cards Grid -->
                    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Serialized ONU Inventory</span>
                                <i class="fa-solid fa-box text-emerald-400"></i>
                            </div>
                            <h3 id="stat_inventory_cnt" class="text-3xl font-bold mt-2 text-white">3</h3>
                            <p class="text-xs text-slate-300 mt-1">Serialized units stored across regions.</p>
                        </div>
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Total Invoiced Amount</span>
                                <i class="fa-solid fa-money-bill-wave text-amber-400"></i>
                            </div>
                            <h3 id="stat_finance_val" class="text-3xl font-bold mt-2 text-white">KSh 20,000</h3>
                            <p class="text-xs text-slate-300 mt-1">Total outstanding + paid balances.</p>
                        </div>
                        <div class="bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <div class="flex items-center justify-between text-slate-400">
                                <span class="text-xs font-bold uppercase">Pending Approvals</span>
                                <i class="fa-solid fa-clock-rotate-left text-sky-400"></i>
                            </div>
                            <h3 id="stat_tasks_cnt" class="text-3xl font-bold mt-2 text-white">1</h3>
                            <p class="text-xs text-slate-300 mt-1">Subordinate timesheets awaiting manager check.</p>
                        </div>
                    </div>

                    <!-- Layout sidebars previewing recent SQLite compliance log -->
                    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
                        <div class="lg:col-span-8 bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md">
                            <h3 class="font-bold text-lg text-emerald-300 mb-2 flex items-center space-x-2">
                                <i class="fa-solid fa-shield-halved"></i>
                                <span>Policy Shield Verification Activity</span>
                            </h3>
                            <p class="text-sm text-slate-300 mb-4">Every action on the dashboard requires both client-side and backend NATS-coupled validation checks. Switching personas changes what is accessible.</p>
                            <a href="/api/exports/excel" target="_blank" class="inline-block bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold py-2.5 px-4 rounded-lg transition duration-200">
                                <i class="fa-solid fa-download mr-1"></i> Export Live SQLite Log to Excel
                            </a>
                        </div>
                        <div class="lg:col-span-4 bg-brand-500 p-6 rounded-xl border border-brand-600 shadow-md flex flex-col h-[280px]">
                            <h4 class="font-bold text-xs uppercase text-slate-400 mb-3 tracking-wider">System Live Audit Log</h4>
                            <div id="miniSqliteLogs" class="flex-1 overflow-y-auto space-y-2 pr-1 font-mono text-[10px] text-slate-300">
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Module B: Inventory View -->
                <div id="view_inventory" class="hidden space-y-6">
                    <div id="inventorySection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2">
                                <i class="fa-solid fa-boxes-stacked text-emerald-400"></i>
                                <span>Serialized Inventory Asset Tracking</span>
                            </h3>
                            <span id="inventoryHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">STRICT SERIAL CONTROL</span>
                        </div>

                        <form id="createItemForm" onsubmit="createInventoryItem(event)" class="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Item Name</label>
                                <input type="text" id="itemName" placeholder="Huawei GPON ONU" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Serial Number</label>
                                <input type="text" id="itemSerial" placeholder="SN-HUA-7700" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                            </div>
                            <div class="flex items-end">
                                <button id="createItemBtn" type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
                                    Create Serial Asset
                                </button>
                            </div>
                        </form>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[600px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Item ID</th>
                                        <th class="py-3 px-4">Name</th>
                                        <th class="py-3 px-4">Serial #</th>
                                        <th class="py-3 px-4">Status</th>
                                        <th class="py-3 px-4">Allocation</th>
                                        <th class="py-3 px-4">Action</th>
                                    </tr>
                                </thead>
                                <tbody id="inventoryTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module C: Finance View -->
                <div id="view_finance" class="hidden space-y-6">
                    <div id="financeSection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2">
                                <i class="fa-solid fa-file-invoice-dollar text-amber-400"></i>
                                <span>Finance &amp; M-Pesa Micro-Payments</span>
                            </h3>
                            <span id="financeHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">TRUST-BASED RECONCILIATION</span>
                        </div>

                        <form id="recordPaymentForm" onsubmit="recordPayment(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Invoice ID</label>
                                <select id="payInvoiceId" class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                                </select>
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">Amount (KSh)</label>
                                <input type="number" id="payAmount" placeholder="3000" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div>
                                <label class="block text-xs text-slate-400 font-bold mb-1">M-Pesa Reference</label>
                                <input type="text" id="payRef" placeholder="QRE99816AH" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none">
                            </div>
                            <div class="flex items-end">
                                <button id="paymentBtn" type="submit" class="w-full bg-amber-600 hover:bg-amber-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
                                    Push M-Pesa STK
                                </button>
                            </div>
                        </form>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[600px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Invoice ID</th>
                                        <th class="py-3 px-4">Total Amount</th>
                                        <th class="py-3 px-4">Paid Amount</th>
                                        <th class="py-3 px-4">Balance Due</th>
                                        <th class="py-3 px-4">Status</th>
                                    </tr>
                                </thead>
                                <tbody id="financeTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module D: Tasks/Timesheets View -->
                <div id="view_tasks" class="hidden space-y-6">
                    <div id="tasksSection" class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4">
                            <h3 class="font-bold text-lg flex items-center space-x-2">
                                <i class="fa-solid fa-list-check text-sky-400"></i>
                                <span>Field Technician Timesheets &amp; Tasks</span>
                            </h3>
                            <span id="tasksHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">HIERARCHICAL APPROVALS</span>
                        </div>

                        <div class="overflow-x-auto w-full max-w-full block">
                            <table class="w-full text-left text-sm min-w-[600px]">
                                <thead>
                                    <tr class="border-b border-brand-600 text-slate-400 text-xs uppercase">
                                        <th class="py-3 px-4">Timesheet ID</th>
                                        <th class="py-3 px-4">Task ID</th>
                                        <th class="py-3 px-4">Technician</th>
                                        <th class="py-3 px-4">Logged Hours</th>
                                        <th class="py-3 px-4">Status</th>
                                        <th class="py-3 px-4">Approved By</th>
                                        <th class="py-3 px-4">Action</th>
                                    </tr>
                                </thead>
                                <tbody id="timesheetsTableBody" class="divide-y divide-brand-600">
                                </tbody>
                            </table>
                        </div>
                    </div>
                </div>

                <!-- Module E: RBAC Policy View -->
                <div id="view_rbac" class="hidden space-y-6">
                    <div class="bg-brand-500 rounded-xl border border-brand-600 p-4 md:p-6 shadow-sm">
                        <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-3">
                            <h3 class="font-bold text-lg flex items-center space-x-2 text-emerald-400">
                                <i class="fa-solid fa-shield-halved"></i>
                                <span>Interactive Policy Engine Manager (ABAC/RBAC)</span>
                            </h3>
                            <span class="text-xs px-2 py-0.5 bg-brand-900 text-emerald-400 font-mono rounded font-bold border border-brand-600">TenantAdmin Control Only</span>
                        </div>
                        <p class="text-xs text-slate-300 mb-4">Toggle dynamic role permissions in the database instantly. When updated, Go's hierarchy resolver re-calculates all backend and frontend capabilities in real time.</p>

                        <div id="policyConfigGrid" class="grid grid-cols-1 md:grid-cols-2 gap-4">
                        </div>
                    </div>
                </div>

            </div>
        </div>
    </div>

    <!-- REGISTER TENANT MODAL -->
    <div id="registerTenantModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Register New Tenant Subscriber</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Tenant ID (Unique Key)</label>
                <input type="text" id="regTenantId" placeholder="tenant_pioneer_isp" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Tenant Enterprise Name</label>
                <input type="text" id="regTenantName" placeholder="Pioneer ISP Tech Services" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeRegisterTenantModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitTenantRegistration()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Submit Registration
                </button>
            </div>
        </div>
    </div>

    <!-- REGISTER USER MODAL -->
    <div id="registerUserModal" class="hidden fixed inset-0 bg-black bg-opacity-80 flex items-center justify-center p-4 z-50 animate-fade-in">
        <div class="bg-brand-500 rounded-2xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Create New User Profile</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">User ID</label>
                <input type="text" id="regUserId" placeholder="usr_karanja" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Full Name</label>
                <input type="text" id="regUserName" placeholder="David Karanja" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Tenant</label>
                <select id="regUserTenantSelect" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                </select>
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Assign Role</label>
                <select id="regUserRoleSelect" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                    <option value="tenant_admin">tenant_admin (Alice level)</option>
                    <option value="manager">manager (Bob level)</option>
                    <option value="finance_officer">finance_officer (Eva level)</option>
                    <option value="field_technician">field_technician (Charlie level)</option>
                </select>
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Working Region</label>
                <input type="text" id="regUserRegion" placeholder="Mombasa" required class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeRegisterUserModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitUserRegistration()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Create Profile
                </button>
            </div>
        </div>
    </div>

    <!-- ALLOCATION MODAL -->
    <div id="allocationModal" class="hidden fixed inset-0 bg-black bg-opacity-70 flex items-center justify-center p-4 z-50">
        <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Allocate Device to Technician</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Item ID</label>
                <input type="text" id="modalItemId" readonly class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-400 focus:outline-none">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Technician</label>
                <select id="modalTechId" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200 focus:outline-none">
                </select>
            </div>
            <div class="flex space-x-2 pt-2 justify-end">
                <button onclick="closeModal()" class="bg-brand-900 hover:bg-brand-600 text-slate-300 text-xs py-2 px-4 rounded font-bold">
                    Cancel
                </button>
                <button onclick="submitAllocation()" class="bg-emerald-600 hover:bg-emerald-500 text-white text-xs py-2 px-4 rounded font-bold">
                    Allocate Asset
                </button>
            </div>
        </div>
    </div>

    <script>
        let currentState = {};
        let currentHeaders = {};
        let activeView = 'dashboard';
        let loggedIn = false;
        let recentNotifications = [];
        let mobileSidebarOpen = false;

        async function initAuth() {
            try {
                const res = await fetch('/api/state', {
                    headers: { 'Authorization-Tenant-Id': 'tenant_safari', 'Authorization-User-Id': 'usr_safari_admin', 'Authorization-Roles': 'tenant_admin', 'Authorization-Region': 'Nairobi' }
                });
                const data = await res.json();
                currentState = data;

                const loginSelect = document.getElementById('loginUserSelect');
                loginSelect.innerHTML = '';

                Object.values(currentState.users || {}).forEach(u => {
                    const opt = document.createElement('option');
                    opt.value = u.id;
                    const tenantName = currentState.tenants[u.tenant_id] ? currentState.tenants[u.tenant_id].name : u.tenant_id;
                    opt.text = u.name + ' (' + u.role_name + ' @ ' + tenantName + ')';
                    loginSelect.appendChild(opt);
                });
            } catch (err) {
                console.error('Failed to init auth options:', err);
            }
        }

        function handleLogin() {
            const loginSelect = document.getElementById('loginUserSelect');
            const selectedUserId = loginSelect.value;
            const u = currentState.users[selectedUserId];

            if (!u) {
                alert('Invalid profile selection.');
                return;
            }

            loggedIn = true;
            currentHeaders = {
                'Authorization-Tenant-Id': u.tenant_id,
                'Authorization-User-Id': u.id,
                'Authorization-Roles': u.role_name,
                'Authorization-Region': u.region,
                'Content-Type': 'application/json'
            };

            // Transition screens
            document.getElementById('loginGate').classList.add('hidden');
            document.getElementById('dashboardApp').classList.remove('hidden');

            // Render details inside Sidebar profile box
            document.getElementById('sidebarUserName').innerText = u.name;
            document.getElementById('sidebarRoleLabel').innerText = u.role_name;
            document.getElementById('sidebarAvatar').innerText = u.name.charAt(0);

            const tenantObj = currentState.tenants[u.tenant_id];
            const tenantNameStr = tenantObj ? tenantObj.name : u.tenant_id;
            document.getElementById('sidebarTenantName').innerText = tenantNameStr;
            document.getElementById('mobileTenantTitle').innerText = tenantNameStr;

            pushNotification('SESSION_LOGGED_IN', 'User ' + u.name + ' entered the ' + tenantNameStr + ' workspace.');

            switchModuleView('dashboard');
            fetchState();
            fetchLogs();
        }

        function handleLogout() {
            loggedIn = false;
            document.getElementById('dashboardApp').classList.add('hidden');
            document.getElementById('loginGate').classList.remove('hidden');
            closeMobileSidebar();
            initAuth();
        }

        // Toggle mobile responsive sidebar
        function toggleMobileSidebar() {
            const sidebar = document.getElementById('sidebarDrawer');
            const icon = document.getElementById('hamburgerIcon');

            if (mobileSidebarOpen) {
                sidebar.classList.add('hidden');
                sidebar.classList.remove('flex', 'absolute', 'w-64', 'h-screen');
                icon.className = 'fa-solid fa-bars text-lg';
                mobileSidebarOpen = false;
            } else {
                sidebar.classList.remove('hidden');
                sidebar.classList.add('flex', 'absolute', 'w-64', 'h-screen');
                icon.className = 'fa-solid fa-xmark text-lg';
                mobileSidebarOpen = true;
            }
        }

        function closeMobileSidebar() {
            if (mobileSidebarOpen) {
                toggleMobileSidebar();
            }
        }

        function switchModuleView(viewName) {
            activeView = viewName;

            // Hide all sub-views
            const views = ['dashboard', 'inventory', 'finance', 'tasks', 'rbac'];
            views.forEach(v => {
                document.getElementById('view_' + v).classList.add('hidden');

                // Revert navigation class styles
                const link = document.getElementById('navLink_' + v);
                link.className = 'w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg text-slate-300 hover:bg-brand-600 hover:text-white transition';
            });

            // Show active view
            document.getElementById('view_' + viewName).classList.remove('hidden');

            // Stylize navigation link
            const link = document.getElementById('navLink_' + viewName);
            link.className = 'w-full flex items-center space-x-3 px-4 py-2.5 rounded-lg bg-emerald-600 text-white font-bold transition';

            // Update Module Header Title
            let title = 'Dashboard Home';
            if (viewName === 'inventory') title = 'Inventory Serial Tracking';
            else if (viewName === 'finance') title = 'Finance &amp; M-Pesa payments';
            else if (viewName === 'tasks') title = 'Technician Timesheets';
            else if (viewName === 'rbac') title = 'RBAC Policy Engine';

            document.getElementById('currentModuleTitle').innerHTML = title;
            closeMobileSidebar();
        }

        function pushNotification(type, message) {
            recentNotifications.unshift({
                timestamp: new Date().toLocaleTimeString(),
                type: type,
                message: message
            });
            if (recentNotifications.length > 20) recentNotifications.pop();

            // Toggle badge alert
            document.getElementById('notifBadge').classList.remove('hidden');
            document.getElementById('mobileNotifBadge').classList.remove('hidden');
            renderNotifications();
        }

        function toggleNotificationPane() {
            const pane = document.getElementById('notifPane');
            pane.classList.toggle('hidden');
            document.getElementById('notifBadge').classList.add('hidden');
            document.getElementById('mobileNotifBadge').classList.add('hidden');
        }

        function clearNotifications() {
            recentNotifications = [];
            renderNotifications();
        }

        function renderNotifications() {
            const container = document.getElementById('notifList');
            container.innerHTML = '';

            if (recentNotifications.length === 0) {
                container.innerHTML = '<div class="p-2 bg-brand-900 rounded border border-brand-600 italic text-slate-500">No recent notifications logged.</div>';
                return;
            }

            recentNotifications.forEach(n => {
                const div = document.createElement('div');
                div.className = 'p-2 bg-brand-900 rounded border border-brand-600 flex items-center justify-between';
                div.innerHTML = '<span>[' + n.timestamp + '] <strong class="text-white">' + n.type + ':</strong> ' + n.message + '</span>';
                container.appendChild(div);
            });
        }

        // Fast Registration Modal control triggers
        function openRegisterTenantModal() {
            document.getElementById('registerTenantModal').classList.remove('hidden');
        }
        function closeRegisterTenantModal() {
            document.getElementById('registerTenantModal').classList.add('hidden');
        }
        async function submitTenantRegistration() {
            const id = document.getElementById('regTenantId').value;
            const name = document.getElementById('regTenantName').value;

            if (!id || !name) {
                alert('Both ID and Name are required.');
                return;
            }

            try {
                const res = await fetch('/api/tenants', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ id: id, name: name })
                });
                const r = await res.json();
                if (res.ok) {
                    alert(r.message);
                    closeRegisterTenantModal();
                    initAuth();
                } else {
                    alert('Error: ' + r.error);
                }
            } catch (err) {
                console.error(err);
            }
        }

        function openRegisterUserModal() {
            const sel = document.getElementById('regUserTenantSelect');
            sel.innerHTML = '';
            Object.values(currentState.tenants || {}).forEach(t => {
                const opt = document.createElement('option');
                opt.value = t.id;
                opt.text = t.name;
                sel.appendChild(opt);
            });

            document.getElementById('registerUserModal').classList.remove('hidden');
        }
        function closeRegisterUserModal() {
            document.getElementById('registerUserModal').classList.add('hidden');
        }
        async function submitUserRegistration() {
            const id = document.getElementById('regUserId').value;
            const name = document.getElementById('regUserName').value;
            const tenantId = document.getElementById('regUserTenantSelect').value;
            const role = document.getElementById('regUserRoleSelect').value;
            const region = document.getElementById('regUserRegion').value;

            if (!id || !name || !tenantId || !role || !region) {
                alert('All user registration fields are required.');
                return;
            }

            try {
                const res = await fetch('/api/users', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ id, tenant_id: tenantId, name, role_name: role, region })
                });
                const r = await res.json();
                if (res.ok) {
                    alert(r.message);
                    closeRegisterUserModal();
                    initAuth();
                } else {
                    alert('Error: ' + r.error);
                }
            } catch (err) {
                console.error(err);
            }
        }

        async function fetchState() {
            if (!loggedIn) return;
            try {
                const res = await fetch('/api/state', { headers: currentHeaders });
                const data = await res.json();
                currentState = data;
                renderDashboard();
            } catch (err) {
                console.error('Error loading state:', err);
            }
        }

        async function fetchLogs() {
            if (!loggedIn) return;
            try {
                const res = await fetch('/api/logs', { headers: currentHeaders });
                const logs = await res.json();

                const miniContainer = document.getElementById('miniSqliteLogs');
                miniContainer.innerHTML = '';

                if (!logs || logs.length === 0) {
                    miniContainer.innerHTML = "<div class='text-slate-500 italic'>No logs found in SQLite.</div>";
                    return;
                }

                logs.slice(0, 10).forEach(l => {
                    const div = document.createElement('div');
                    div.className = 'p-1.5 bg-brand-900 rounded border border-brand-600 flex flex-col space-y-0.5';
                    const t = new Date(l.timestamp).toLocaleTimeString();

                    div.innerHTML = '<div class="flex justify-between font-bold text-slate-400"><span>[' + t + '] ' + l.action + '</span></div>' +
                        '<div class="text-slate-400">' + l.details + '</div>';
                    miniContainer.appendChild(div);
                });
            } catch (err) {
                console.error('Error loading SQLite logs:', err);
            }
        }

        async function togglePermission(roleName, perm, checkbox) {
            const currentRole = currentHeaders['Authorization-Roles'];
            if (currentRole !== 'tenant_admin') {
                alert('Permission Denied: Only a TenantAdmin is authorized to update RBAC policies.');
                checkbox.checked = !checkbox.checked;
                return;
            }

            const grid = document.getElementById('policyConfigGrid');
            const checkboxes = grid.querySelectorAll('input[data-role="' + roleName + '"]:checked');
            const newPerms = [];
            checkboxes.forEach(cb => {
                newPerms.push(cb.value);
            });

            try {
                const res = await fetch('/api/rbac/update', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ role_name: roleName, permissions: newPerms })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('RBAC_MUTATED', 'Role ' + roleName + ' permission set successfully toggled.');
                    fetchState();
                } else {
                    alert('Error: ' + r.error);
                }
            } catch (err) {
                console.error('Failed to toggle permission:', err);
            }
        }

        function renderDashboard() {
            const activeRole = currentHeaders['Authorization-Roles'];
            const activeTenant = currentHeaders['Authorization-Tenant-Id'];

            // Fill Quick stat card numbers
            let invCount = 0;
            Object.values(currentState.inventory || {}).forEach(item => { if (item.tenant_id === activeTenant) invCount++; });
            document.getElementById('stat_inventory_cnt').innerText = invCount;

            let finSum = 0;
            Object.values(currentState.invoices || {}).forEach(inv => { if (inv.tenant_id === activeTenant) finSum += inv.total_amount; });
            document.getElementById('stat_finance_val').innerText = 'KSh ' + finSum.toLocaleString();

            let pendingApprovals = 0;
            Object.values(currentState.timesheets || {}).forEach(ts => { if (ts.tenant_id === activeTenant && ts.status === 'Submitted') pendingApprovals++; });
            document.getElementById('stat_tasks_cnt').innerText = pendingApprovals;

            // 1. Draw Policy Manager (RBAC/ABAC UI view)
            const policyGrid = document.getElementById('policyConfigGrid');
            policyGrid.innerHTML = '';

            const allPermsList = ['inventory:read', 'inventory:write', 'inventory:*', 'finance:read', 'finance:write', 'tasks:read', 'tasks:create', 'tasks:approve', 'timesheets:approve', 'timesheets:submit', '*'];

            const rolesKeys = Object.keys(currentState.roles || {}).sort();
            rolesKeys.forEach(roleName => {
                const rolePerms = currentState.roles[roleName] || [];
                const card = document.createElement('div');
                card.className = 'bg-brand-900 border border-brand-600 rounded-lg p-4 space-y-2';

                let titleColor = 'text-sky-300';
                if (roleName === 'tenant_admin') titleColor = 'text-rose-400 font-bold';
                else if (roleName === 'manager') titleColor = 'text-amber-400';

                card.innerHTML = '<div class="text-xs uppercase font-bold tracking-widest ' + titleColor + '">' + roleName + '</div>';

                const cbContainer = document.createElement('div');
                cbContainer.className = 'grid grid-cols-2 gap-x-2 gap-y-1';

                allPermsList.forEach(perm => {
                    const isChecked = rolePerms.includes(perm) || rolePerms.includes('*') ? 'checked' : '';

                    const label = document.createElement('label');
                    label.className = 'flex items-center space-x-2 text-[11px] text-slate-300 cursor-pointer hover:text-slate-100';
                    const disabledStr = activeRole === 'tenant_admin' ? '' : 'disabled';

                    label.innerHTML = '<input type="checkbox" value="' + perm + '" data-role="' + roleName + '" ' + isChecked + ' ' + disabledStr + ' onchange="togglePermission(\'' + roleName + '\', \'' + perm + '\', this)" class="rounded bg-brand-500 border-brand-600 text-emerald-500 focus:ring-emerald-500"> ' +
                        '<span>' + perm + '</span>';
                    cbContainer.appendChild(label);
                });

                card.appendChild(cbContainer);
                policyGrid.appendChild(card);
            });

            // 2. Fetch inventory, invoice select options
            const paySelect = document.getElementById('payInvoiceId');
            paySelect.innerHTML = '';

            // Render Inventory Section
            const invBody = document.getElementById('inventoryTableBody');
            invBody.innerHTML = '';

            const activeRolePermissions = currentState.roles[activeRole] || [];
            const canViewInventory = activeRolePermissions.includes('inventory:read') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');
            const canWriteInventory = activeRolePermissions.includes('inventory:write') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');

            const invSection = document.getElementById('inventorySection');
            const createItemForm = document.getElementById('createItemForm');

            if (!canViewInventory) {
                invSection.classList.add('opacity-50');
                document.getElementById('inventoryHeaderBadge').innerHTML = '<span class="text-rose-400 font-bold">ACCESS DENIED</span>';
                invBody.innerHTML = '<tr><td colspan="6" class="py-4 text-center text-xs text-rose-400 italic">Role ' + activeRole + ' does not have permission (inventory:read) to view serialized assets.</td></tr>';
            } else {
                invSection.classList.remove('opacity-50');
                document.getElementById('inventoryHeaderBadge').innerText = 'STRICT SERIAL CONTROL';
            }

            if (!canWriteInventory) {
                createItemForm.classList.add('opacity-40', 'pointer-events-none');
                document.getElementById('createItemBtn').disabled = true;
            } else {
                createItemForm.classList.remove('opacity-40', 'pointer-events-none');
                document.getElementById('createItemBtn').disabled = false;
            }

            if (canViewInventory) {
                Object.values(currentState.inventory || {}).forEach(item => {
                    if (item.tenant_id !== activeTenant) return;

                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    const statusColor = item.status === 'In_Stock' ? 'text-emerald-400 font-semibold' : 'text-sky-400';
                    const allocText = item.assigned_to ? item.assigned_to : '<span class=\'text-slate-500\'>Unassigned</span>';

                    const assignBtn = item.status === 'In_Stock'
                        ? '<button onclick="openAllocationModal(\'' + item.id + '\')" ' + (canWriteInventory ? '' : 'disabled') + ' class="text-xs bg-brand-900 text-emerald-400 hover:bg-brand-500 border border-brand-600 rounded py-1 px-2 font-bold transition disabled:opacity-40">Allocate</button>'
                        : '<span class="text-xs text-slate-500">Allocated</span>';

                    tr.innerHTML = '<td class="py-3 px-4 font-mono">' + item.id + '</td>' +
                        '<td class="py-3 px-4">' + item.name + '</td>' +
                        '<td class="py-3 px-4 font-mono">' + item.serial_number + '</td>' +
                        '<td class="py-3 px-4 ' + statusColor + '">' + item.status + '</td>' +
                        '<td class="py-3 px-4 text-emerald-300 font-medium">' + allocText + '</td>' +
                        '<td class="py-3 px-4">' + assignBtn + '</td>';
                    invBody.appendChild(tr);
                });
            }

            // Render Finance Section (ABAC Checks)
            const canViewFinance = activeRolePermissions.includes('finance:read') || activeRolePermissions.includes('finance:*') || activeRolePermissions.includes('*');
            const canWriteFinance = activeRolePermissions.includes('finance:write') || activeRolePermissions.includes('finance:*') || activeRolePermissions.includes('*');

            const financeSection = document.getElementById('financeSection');
            const recordPaymentForm = document.getElementById('recordPaymentForm');
            const finBody = document.getElementById('financeTableBody');
            finBody.innerHTML = '';

            if (!canViewFinance) {
                financeSection.classList.add('opacity-50');
                document.getElementById('financeHeaderBadge').innerHTML = '<span class="text-rose-400 font-bold">ACCESS DENIED</span>';
                finBody.innerHTML = '<tr><td colspan="5" class="py-4 text-center text-xs text-rose-400 italic">Role ' + activeRole + ' does not have permission (finance:read) to view credit invoices.</td></tr>';
            } else {
                financeSection.classList.remove('opacity-50');
                document.getElementById('financeHeaderBadge').innerText = 'TRUST-BASED RECONCILIATION';
            }

            if (!canWriteFinance) {
                recordPaymentForm.classList.add('opacity-40', 'pointer-events-none');
                document.getElementById('paymentBtn').disabled = true;
            } else {
                recordPaymentForm.classList.remove('opacity-40', 'pointer-events-none');
                document.getElementById('paymentBtn').disabled = false;
            }

            if (canViewFinance) {
                Object.values(currentState.invoices || {}).forEach(inv => {
                    if (inv.tenant_id !== activeTenant) return;

                    const opt = document.createElement('option');
                    opt.value = inv.id;
                    opt.text = inv.id + ' (Bal: KSh ' + inv.balance_amount + ')';
                    paySelect.appendChild(opt);

                    const tr = document.createElement('tr');
                    tr.className = 'hover:bg-brand-600 transition';

                    const statusColor = inv.status === 'Paid' ? 'text-emerald-400 font-bold' : (inv.status === 'Partially_Paid' ? 'text-amber-400' : 'text-slate-300');

                    tr.innerHTML = '<td class="py-3 px-4 font-bold">' + inv.id + '</td>' +
                        '<td class="py-3 px-4 font-mono">KSh ' + inv.total_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 font-mono">KSh ' + inv.paid_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 font-mono text-rose-400">KSh ' + inv.balance_amount.toFixed(2) + '</td>' +
                        '<td class="py-3 px-4 ' + statusColor + '">' + inv.status + '</td>';
                    finBody.appendChild(tr);
                });
            }

            // Render Tasks & Timesheets Section (Hierarchy ABAC Checks)
            const tsBody = document.getElementById('timesheetsTableBody');
            tsBody.innerHTML = '';

            const canApproveTimesheets = activeRolePermissions.includes('timesheets:approve') || activeRolePermissions.includes('tasks:*') || activeRolePermissions.includes('*');

            Object.values(currentState.timesheets || {}).forEach(ts => {
                if (ts.tenant_id !== activeTenant) return;

                const tr = document.createElement('tr');
                tr.className = 'hover:bg-brand-600 transition';

                const approveBtn = ts.status === 'Submitted'
                    ? '<button onclick="approveTimesheet(\'' + ts.id + '\')" ' + (canApproveTimesheets ? '' : 'disabled') + ' class="text-xs bg-sky-600 hover:bg-sky-500 text-white font-bold py-1 px-2 rounded disabled:opacity-40">Approve</button>'
                    : '<span class="text-slate-500 text-xs">Approved</span>';

                tr.innerHTML = '<td class="py-3 px-4 font-mono">' + ts.id + '</td>' +
                    '<td class="py-3 px-4 font-mono">' + ts.task_id + '</td>' +
                    '<td class="py-3 px-4 text-emerald-300 font-medium">' + ts.user_id + '</td>' +
                    '<td class="py-3 px-4 font-bold">' + ts.hours + ' hrs</td>' +
                    '<td class="py-3 px-4">' + ts.status + '</td>' +
                    '<td class="py-3 px-4 text-slate-400 font-medium">' + (ts.approved_by || '-') + '</td>' +
                    '<td class="py-3 px-4">' + approveBtn + '</td>';
                tsBody.appendChild(tr);
            });
        }

        async function createInventoryItem(e) {
            e.preventDefault();
            const name = document.getElementById('itemName').value;
            const sn = document.getElementById('itemSerial').value;

            try {
                const res = await fetch('/api/inventory', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ name, serial_number: sn })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('INVENTORY_QUEUED', 'Creation command published to SALES stream.');
                    document.getElementById('itemName').value = '';
                    document.getElementById('itemSerial').value = '';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed item create:', err);
            }
        }

        async function recordPayment(e) {
            e.preventDefault();
            const invoiceID = document.getElementById('payInvoiceId').value;
            const amount = parseFloat(document.getElementById('payAmount').value);
            const ref = document.getElementById('payRef').value;

            try {
                const res = await fetch('/api/finance/payments', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ invoice_id: invoiceID, amount, payment_method: 'Mpesa_Paybill', reference: ref })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('MPESA_QUEUED', 'M-Pesa payment published to FINANCE bus.');
                    document.getElementById('payAmount').value = '';
                    document.getElementById('payRef').value = '';
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed payment recording:', err);
            }
        }

        async function approveTimesheet(timesheetId) {
            try {
                const res = await fetch('/api/tasks/timesheets/approve', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ timesheet_id: timesheetId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('TIMESHEET_QUEUED', 'Approval of timesheet ' + timesheetId + ' published to NATS.');
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Authorization Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed timesheet approval:', err);
            }
        }

        function openAllocationModal(itemId) {
            document.getElementById('modalItemId').value = itemId;
            const techSelect = document.getElementById('modalTechId');
            techSelect.innerHTML = '';
            const currentTenant = currentHeaders['Authorization-Tenant-Id'];

            Object.values(currentState.users || {}).forEach(u => {
                if (u.tenant_id === currentTenant && u.role_name === 'field_technician') {
                    const opt = document.createElement('option');
                    opt.value = u.id;
                    opt.text = u.name + ' (Region: ' + u.region + ')';
                    techSelect.appendChild(opt);
                }
            });

            document.getElementById('allocationModal').classList.remove('hidden');
        }

        function closeModal() {
            document.getElementById('allocationModal').classList.add('hidden');
        }

        async function submitAllocation() {
            const itemId = document.getElementById('modalItemId').value;
            const techId = document.getElementById('modalTechId').value;

            try {
                const res = await fetch('/api/inventory/assign', {
                    method: 'POST',
                    headers: currentHeaders,
                    body: JSON.stringify({ item_id: itemId, user_id: techId })
                });
                const r = await res.json();
                if (res.ok) {
                    pushNotification('ASSIGN_QUEUED', 'Device allocation command published successfully.');
                    closeModal();
                    setTimeout(fetchState, 1500);
                } else {
                    alert('Error: ' + (r.error || r.message));
                }
            } catch (err) {
                console.error('Failed assign device:', err);
            }
        }

        setInterval(() => {
            fetchState();
            fetchLogs();
        }, 5000);

        initAuth();
    </script>
</body>
</html>
`
