package handler

import (
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

// ServeDashboard returns the gorgeous fully-functional Tailwind UI
func (h *UIHandler) ServeDashboard(c echo.Context) error {
	return c.HTML(http.StatusOK, htmlContent)
}

// GetState returns the current in-memory DB state for UI rendering
func (h *UIHandler) GetState(c echo.Context) error {
	state := h.db.GetState()
	return c.JSON(http.StatusOK, state)
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
                <div class="bg-brand-500 rounded-xl p-6 border border-brand-600 shadow-md flex items-center justify-between">
                    <div>
                        <h2 class="text-lg font-bold text-emerald-300">RBAC &amp; ABAC Dynamic Protection</h2>
                        <p class="text-sm text-slate-300 mt-1">Tenant isolation, regional access bounds, and hierarchical task clearance are evaluated in real-time for every client request.</p>
                    </div>
                    <div class="px-3 py-1 bg-brand-900 border border-brand-600 text-xs font-mono rounded text-emerald-400" id="currentRoleTag">
                        tenant_admin
                    </div>
                </div>

                <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
                    <div class="flex items-center justify-between mb-4">
                        <h3 class="font-bold text-lg flex items-center space-x-2">
                            <i class="fa-solid fa-boxes-stacked text-emerald-400"></i>
                            <span>Serialized Inventory Asset Tracking</span>
                        </h3>
                        <span class="text-xs text-slate-400 uppercase tracking-widest">STRICT SERIAL CONTROL</span>
                    </div>

                    <form onsubmit="createInventoryItem(event)" class="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
                        <div>
                            <label class="block text-xs text-slate-400 font-bold mb-1">Item Name</label>
                            <input type="text" id="itemName" placeholder="Huawei GPON ONU" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 font-bold mb-1">Serial Number</label>
                            <input type="text" id="itemSerial" placeholder="SN-HUA-7700" required class="w-full bg-brand-500 border border-brand-600 rounded px-3 py-2 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-emerald-400">
                        </div>
                        <div class="flex items-end">
                            <button type="submit" class="w-full bg-emerald-600 hover:bg-emerald-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
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

                <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
                    <div class="flex items-center justify-between mb-4">
                        <h3 class="font-bold text-lg flex items-center space-x-2">
                            <i class="fa-solid fa-file-invoice-dollar text-amber-400"></i>
                            <span>Finance &amp; M-Pesa Micro-Payments</span>
                        </h3>
                        <span class="text-xs text-slate-400 uppercase tracking-widest">TRUST-BASED RECONCILIATION</span>
                    </div>

                    <form onsubmit="recordPayment(event)" class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6 p-4 bg-brand-900 rounded-lg border border-brand-600">
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
                            <button type="submit" class="w-full bg-amber-600 hover:bg-amber-500 text-white text-sm font-bold py-2 px-4 rounded transition duration-200">
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

                <div class="bg-brand-500 rounded-xl border border-brand-600 p-6 shadow-sm">
                    <div class="flex items-center justify-between mb-4">
                        <h3 class="font-bold text-lg flex items-center space-x-2">
                            <i class="fa-solid fa-list-check text-sky-400"></i>
                            <span>Field Technician Timesheets &amp; Tasks</span>
                        </h3>
                        <span class="text-xs text-slate-400 uppercase tracking-widest">HIERARCHICAL APPROVALS</span>
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

        function renderDashboard() {
            const paySelect = document.getElementById('payInvoiceId');
            paySelect.innerHTML = '';
            const currentTenant = currentHeaders['Authorization-Tenant-Id'];

            const invBody = document.getElementById('inventoryTableBody');
            invBody.innerHTML = '';
            Object.values(currentState.inventory || {}).forEach(item => {
                if (item.tenant_id !== currentTenant) return;

                const tr = document.createElement('tr');
                tr.className = 'hover:bg-brand-600 transition';

                const statusColor = item.status === 'In_Stock' ? 'text-emerald-400 font-semibold' : 'text-sky-400';
                const allocText = item.assigned_to ? item.assigned_to : '<span class=\'text-slate-500\'>Unassigned</span>';

                const assignBtn = item.status === 'In_Stock'
                    ? '<button onclick="openAllocationModal(\'' + item.id + '\')" class="text-xs bg-brand-900 text-emerald-400 hover:bg-brand-500 border border-brand-600 rounded py-1 px-2 font-bold transition">Allocate</button>'
                    : '<span class="text-xs text-slate-500">Allocated</span>';

                tr.innerHTML = '<td>' + item.id + '</td>' +
                    '<td>' + item.name + '</td>' +
                    '<td>' + item.serial_number + '</td>' +
                    '<td class="' + statusColor + '">' + item.status + '</td>' +
                    '<td>' + allocText + '</td>' +
                    '<td>' + assignBtn + '</td>';
                invBody.appendChild(tr);
            });

            const finBody = document.getElementById('financeTableBody');
            finBody.innerHTML = '';
            Object.values(currentState.invoices || {}).forEach(inv => {
                if (inv.tenant_id !== currentTenant) return;

                const opt = document.createElement('option');
                opt.value = inv.id;
                opt.text = inv.id + ' (Bal: KSh ' + inv.balance_amount + ')';
                paySelect.appendChild(opt);

                const tr = document.createElement('tr');
                tr.className = 'hover:bg-brand-600 transition';

                const statusColor = inv.status === 'Paid' ? 'text-emerald-400 font-bold' : (inv.status === 'Partially_Paid' ? 'text-amber-400' : 'text-slate-300');

                tr.innerHTML = '<td>' + inv.id + '</td>' +
                    '<td>KSh ' + inv.total_amount.toFixed(2) + '</td>' +
                    '<td>KSh ' + inv.paid_amount.toFixed(2) + '</td>' +
                    '<td>KSh ' + inv.balance_amount.toFixed(2) + '</td>' +
                    '<td class="' + statusColor + '">' + inv.status + '</td>';
                finBody.appendChild(tr);
            });

            const tsBody = document.getElementById('timesheetsTableBody');
            tsBody.innerHTML = '';
            Object.values(currentState.timesheets || {}).forEach(ts => {
                if (ts.tenant_id !== currentTenant) return;

                const tr = document.createElement('tr');
                tr.className = 'hover:bg-brand-600 transition';

                const approveBtn = ts.status === 'Submitted'
                    ? '<button onclick="approveTimesheet(\'' + ts.id + '\')" class="text-xs bg-sky-600 hover:bg-sky-500 text-white font-bold py-1 px-2 rounded">Approve</button>'
                    : '<span class="text-slate-500 text-xs">Approved</span>';

                tr.innerHTML = '<td>' + ts.id + '</td>' +
                    '<td>' + ts.task_id + '</td>' +
                    '<td>' + ts.user_id + '</td>' +
                    '<td>' + ts.hours + ' hrs</td>' +
                    '<td>' + ts.status + '</td>' +
                    '<td>' + (ts.approved_by || '-') + '</td>' +
                    '<td>' + approveBtn + '</td>';
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
                    alert('Error: ' + r.error);
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
                    alert('Error: ' + r.error);
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
                    alert('Error: ' + r.error);
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
