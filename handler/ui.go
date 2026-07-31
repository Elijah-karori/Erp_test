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

// ServeDashboard returns the gorgeous fully-functional Tailwind UI
func (h *UIHandler) ServeDashboard(c echo.Context) error {
	return c.HTML(http.StatusOK, htmlContent)
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
<body class="bg-brand-900 text-slate-100 min-h-screen font-sans">
    <div class="flex flex-col min-h-screen">
        <header class="bg-brand-500 border-b border-brand-600 px-6 py-4 flex items-center justify-between shadow-lg">
            <div class="flex items-center space-x-3">
                <i class="fa-solid fa-network-wired text-emerald-400 text-2xl"></i>
                <h1 class="text-xl font-bold tracking-wide">SME Kenya ERP &amp; JetStream</h1>
            </div>

            <div class="flex items-center space-x-4 bg-brand-900 px-4 py-2 rounded-lg border border-brand-600">
                <span class="text-xs text-slate-400 font-semibold uppercase">Active Persona:</span>
                <select id="personaSelect" onchange="updateHeaders()" class="bg-brand-500 border-0 text-white text-sm font-medium rounded-md focus:ring-2 focus:ring-emerald-400 focus:border-emerald-400 p-1 cursor-pointer">
                    <option value="usr_safari_admin" data-tenant="tenant_safari" data-role="tenant_admin" data-region="Nairobi">Alice Admin (Safaricom, Admin)</option>
                    <option value="usr_safari_mgr" data-tenant="tenant_safari" data-role="manager" data-region="Nairobi">Bob Manager (Safaricom, Manager)</option>
                    <option value="usr_safari_tech" data-tenant="tenant_safari" data-role="field_technician" data-region="Mombasa">Charlie Tech (Safaricom, Tech)</option>
                    <option value="usr_pioneer_mgr" data-tenant="tenant_pioneer" data-role="manager" data-region="Kisumu">Daniel Manager (Pioneer, Manager)</option>
                    <option value="usr_pioneer_fin" data-tenant="tenant_pioneer" data-role="finance_officer" data-region="Kisumu">Eva Finance (Pioneer, Finance)</option>
                    <option value="usr_pioneer_tech" data-tenant="tenant_pioneer" data-role="field_technician" data-region="Nairobi">Frank Tech (Pioneer, Tech)</option>
                </select>
            </div>
        </header>

        <main class="flex-1 max-w-7xl w-full mx-auto p-6 grid grid-cols-1 lg:grid-cols-12 gap-6">
            <div class="lg:col-span-8 space-y-6">
                <!-- Protection & Active Persona Metrics banner -->
                <div class="bg-brand-500 rounded-xl p-6 border border-brand-600 shadow-md flex items-center justify-between">
                    <div>
                        <h2 class="text-lg font-bold text-emerald-300">RBAC &amp; ABAC Dynamic Protection</h2>
                        <p class="text-sm text-slate-300 mt-1">Tenant isolation, regional access bounds, and hierarchical task clearance are evaluated in real-time for every client request.</p>
                    </div>
                    <div class="px-3 py-1 bg-brand-900 border border-brand-600 text-xs font-mono rounded text-emerald-400" id="currentRoleTag">
                        tenant_admin
                    </div>
                </div>

                <!-- Section: Dynamic RBAC/ABAC Configurator Policy Manager -->
                <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
                    <div class="flex items-center justify-between mb-4 border-b border-brand-600 pb-3">
                        <h3 class="font-bold text-lg flex items-center space-x-2 text-emerald-400">
                            <i class="fa-solid fa-shield-halved"></i>
                            <span>Interactive Policy Engine Manager (ABAC/RBAC)</span>
                        </h3>
                        <span class="text-xs px-2 py-0.5 bg-brand-900 text-emerald-400 font-mono rounded font-bold border border-brand-600">TenantAdmin Control Only</span>
                    </div>
                    <p class="text-xs text-slate-300 mb-4">Toggle dynamic role permissions in the database instantly. When updated, Go's hierarchy resolver re-calculates all backend and frontend capabilities in real time.</p>

                    <div id="policyConfigGrid" class="grid grid-cols-1 md:grid-cols-2 gap-4">
                        <!-- Filled dynamically -->
                    </div>
                </div>

                <!-- Section 1: Serialized Inventory Tracking -->
                <div id="inventorySection" class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
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

                    <div class="overflow-x-auto">
                        <table class="w-full text-left text-sm">
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

                <!-- Section 2: Finance & Payments -->
                <div id="financeSection" class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
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

                    <div class="overflow-x-auto">
                        <table class="w-full text-left text-sm">
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

                <!-- Section 3: Tasks & Hierarchy Timesheets -->
                <div id="tasksSection" class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
                    <div class="flex items-center justify-between mb-4">
                        <h3 class="font-bold text-lg flex items-center space-x-2">
                            <i class="fa-solid fa-list-check text-sky-400"></i>
                            <span>Field Technician Timesheets &amp; Tasks</span>
                        </h3>
                        <span id="tasksHeaderBadge" class="text-xs text-slate-400 uppercase tracking-widest">HIERARCHICAL APPROVALS</span>
                    </div>

                    <div class="overflow-x-auto">
                        <table class="w-full text-left text-sm">
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

            <div class="lg:col-span-4 space-y-6">
                <div class="bg-brand-500 rounded-xl p-6 border border-brand-600 shadow-sm">
                    <h3 class="font-bold text-md text-emerald-400 mb-3 flex items-center space-x-2">
                        <i class="fa-solid fa-file-excel"></i>
                        <span>Excel Export Engine</span>
                    </h3>
                    <p class="text-xs text-slate-300 mb-4">Generate immediate auditor-compliant compliance sheets from database history files.</p>
                    <a href="/api/exports/excel" target="_blank" class="w-full block text-center bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-bold py-3 px-4 rounded transition duration-200">
                        <i class="fa-solid fa-download mr-1"></i> Export Live SQLite Log to Excel
                    </a>
                </div>

                <div class="bg-brand-500 rounded-xl p-6 border border-brand-600 shadow-sm flex flex-col h-[520px]">
                    <div class="flex items-center justify-between mb-4">
                        <h3 class="font-bold text-md text-emerald-400 flex items-center space-x-2">
                            <i class="fa-solid fa-database"></i>
                            <span>SQLite Persistent Audit Log</span>
                        </h3>
                        <button onclick="fetchLogs()" class="text-xs text-emerald-400 hover:underline">
                            <i class="fa-solid fa-arrows-rotate mr-1"></i>Refresh
                        </button>
                    </div>

                    <div id="sqliteLogsContainer" class="flex-1 overflow-y-auto space-y-3 pr-2 font-mono text-xs text-slate-300">
                    </div>
                </div>
            </div>
        </main>

        <footer class="bg-brand-900 border-t border-brand-600 py-4 px-6 text-center text-xs text-slate-500">
            &copy; 2026 Kenyan SME ERP Ecosystem. Powered by Go, Echo, and NATS JetStream.
        </footer>
    </div>

    <div id="allocationModal" class="hidden fixed inset-0 bg-black bg-opacity-70 flex items-center justify-center p-4">
        <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 max-w-sm w-full space-y-4">
            <h4 class="font-bold text-md text-emerald-300">Allocate Device to Technician</h4>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Item ID</label>
                <input type="text" id="modalItemId" readonly class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-400">
            </div>
            <div>
                <label class="block text-xs text-slate-400 font-bold mb-1">Select Technician</label>
                <select id="modalTechId" class="w-full bg-brand-900 border border-brand-600 rounded px-3 py-2 text-sm text-slate-200">
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

        function updateHeaders() {
            const select = document.getElementById('personaSelect');
            const option = select.options[select.selectedIndex];

            const userId = option.value;
            const tenantId = option.getAttribute('data-tenant');
            const role = option.getAttribute('data-role');
            const region = option.getAttribute('data-region');

            currentHeaders = {
                'Authorization-Tenant-Id': tenantId,
                'Authorization-User-Id': userId,
                'Authorization-Roles': role,
                'Authorization-Region': region,
                'Content-Type': 'application/json'
            };

            document.getElementById('currentRoleTag').innerText = role;
            fetchState();
            fetchLogs();
        }

        async function fetchState() {
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
            try {
                const res = await fetch('/api/logs', { headers: currentHeaders });
                const logs = await res.json();
                const container = document.getElementById('sqliteLogsContainer');
                container.innerHTML = '';

                if (!logs || logs.length === 0) {
                    container.innerHTML = "<div class='text-slate-500 italic'>No logs found in SQLite.</div>";
                    return;
                }

                logs.forEach(l => {
                    const div = document.createElement('div');
                    div.className = 'p-3 bg-brand-900 rounded border border-brand-600 flex flex-col space-y-1';
                    const t = new Date(l.timestamp).toLocaleTimeString();

                    div.innerHTML = '<div>' +
                        '<div class="flex items-center justify-between text-slate-400 font-bold">' +
                            '<span>[' + t + '] ' + l.action + '</span>' +
                            '<span class="text-[10px] bg-brand-500 px-1 rounded text-emerald-400">' + l.tenant_id + '</span>' +
                        '</div>' +
                        '<div class="text-emerald-300">By: ' + l.user_id + '</div>' +
                        '<div class="text-slate-400 text-[11px] break-words">' + l.details + '</div>' +
                    '</div>';
                    container.appendChild(div);
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

            // Calculate list of checked permissions for this role
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

                    // Disable inputs if not TenantAdmin to lock control visually
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

            // ABAC: Check inventory clearance view bounds
            const activeRolePermissions = currentState.roles[activeRole] || [];
            const canViewInventory = activeRolePermissions.includes('inventory:read') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');
            const canWriteInventory = activeRolePermissions.includes('inventory:write') || activeRolePermissions.includes('inventory:*') || activeRolePermissions.includes('*');

            const invSection = document.getElementById('inventorySection');
            const createItemForm = document.getElementById('createItemForm');

            // Dynamic RBAC Action Views mapping
            if (!canViewInventory) {
                invSection.classList.add('opacity-50');
                document.getElementById('inventoryHeaderBadge').innerHTML = '<span class="text-rose-400 font-bold">ACCESS DENIED</span>';
                invBody.innerHTML = '<tr><td colspan="6" class="py-4 text-center text-xs text-rose-400 italic">Role ' + activeRole + ' does not have permission (inventory:read) to view serialized assets.</td></tr>';
            } else {
                invSection.classList.remove('opacity-50');
                document.getElementById('inventoryHeaderBadge').innerText = 'STRICT SERIAL CONTROL';
            }

            // Disable or enable Create form visually
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

                    // Button disabled bounds
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
                    alert(r.message);
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
                    alert(r.message);
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
                    alert(r.message);
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
                    alert(r.message);
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

        updateHeaders();
    </script>
</body>
</html>
`
