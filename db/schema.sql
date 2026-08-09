-- ServiceCore-style ERP schema for Supabase (Postgres).
-- Maps 1:1 onto types/types.go. Run in the Supabase SQL editor, or via
-- `supabase db push` / psql against your project's connection string.
--
-- Design notes:
--   * Every tenant-owned table carries tenant_id and an index on it — the
--     app-layer filtering added in db.GetStateForTenant/GetLogsForTenant
--     stays as the primary defense, RLS below is a second, DB-enforced
--     layer so a missed WHERE clause in future code can't leak data.
--   * roles is now keyed by (tenant_id, name) instead of just name — this
--     fixes the global-role issue where editing "manager" permissions for
--     one tenant silently changed every other tenant's "manager" role too.
--   * RLS policies read the caller's tenant from a session variable
--     (app.current_tenant_id) that the Go app must SET on every connection
--     it checks out from the pool, right after authenticating the request
--     and before running any query. See the companion prompt doc for how.

create extension if not exists "pgcrypto"; -- for gen_random_uuid()

-- ============================================================
-- TENANTS
-- ============================================================
create table tenants (
    id         text primary key,
    name       text not null,
    created_at timestamptz not null default now()
);

-- ============================================================
-- ROLES (tenant-scoped — see design note above)
-- ============================================================
create table roles (
    tenant_id   text not null references tenants(id) on delete cascade,
    name        text not null,
    parent_role text,
    permissions text[] not null default '{}',
    primary key (tenant_id, name)
);
create index idx_roles_tenant on roles(tenant_id);

-- ============================================================
-- USERS
-- ============================================================
create table users (
    id            text primary key,
    tenant_id     text not null references tenants(id) on delete cascade,
    name          text not null,
    email         text not null unique, -- login identifier, unique platform-wide
    role_name     text not null,
    region        text not null,
    password_hash text not null,
    manager_id    text references users(id),
    created_at    timestamptz not null default now(),
    foreign key (tenant_id, role_name) references roles(tenant_id, name)
);
create index idx_users_tenant on users(tenant_id);
create index idx_users_email on users(lower(email));

-- ============================================================
-- GOVERNANCE WORKFLOW
-- ============================================================
create table if not exists workflow_instances (
    id text primary key, tenant_id text not null references tenants(id) on delete cascade,
    entity_type text not null, entity_id text not null, workflow_key text not null,
    step integer not null default 1, status text not null default 'REQUESTED',
    requested_by text not null references users(id), checked_by text, executed_by text, reconciled_by text,
    reason text, command_subject text, command_payload jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(), updated_at timestamptz not null default now()
);
alter table workflow_instances add column if not exists command_subject text;
alter table workflow_instances add column if not exists command_payload jsonb not null default '{}'::jsonb;
create index if not exists idx_workflow_tenant_status on workflow_instances(tenant_id,status);
create index if not exists idx_workflow_entity on workflow_instances(tenant_id,entity_type,entity_id);
create table if not exists workflow_decisions (
    id text primary key, instance_id text not null references workflow_instances(id) on delete cascade,
    tenant_id text not null references tenants(id) on delete cascade, actor_id text not null references users(id),
    action text not null, reason text, created_at timestamptz not null default now()
);
create index if not exists idx_workflow_decisions_instance on workflow_decisions(instance_id);
create unique index if not exists uq_workflow_active_entity
    on workflow_instances(tenant_id, entity_type, entity_id)
    where status in ('REQUESTED','APPROVED','EXECUTED');

alter table workflow_instances enable row level security;
alter table workflow_instances force row level security;
drop policy if exists workflow_tenant_isolation on workflow_instances;
create policy workflow_tenant_isolation on workflow_instances
    using (tenant_id = current_setting('app.current_tenant_id', true))
    with check (tenant_id = current_setting('app.current_tenant_id', true));

alter table workflow_decisions enable row level security;
alter table workflow_decisions force row level security;
drop policy if exists workflow_decision_tenant_isolation on workflow_decisions;
create policy workflow_decision_tenant_isolation on workflow_decisions
    using (tenant_id = current_setting('app.current_tenant_id', true))
    with check (tenant_id = current_setting('app.current_tenant_id', true));

-- ============================================================
-- INVITATIONS
-- ============================================================
create table invitations (
    id         text primary key,
    tenant_id  text not null references tenants(id) on delete cascade,
    email      text not null,
    name       text not null,
    role_name  text not null,
    region     text not null,
    manager_id text,
    token      text not null unique,
    status     text not null default 'Pending', -- 'Pending', 'Accepted', 'Expired'
    created_at timestamptz not null default now()
);
create index idx_invitations_tenant on invitations(tenant_id);
create index idx_invitations_token on invitations(token);

-- ============================================================
-- CUSTOMERS
-- ============================================================
create table customers (
    id              text primary key,
    tenant_id       text not null references tenants(id) on delete cascade,
    name            text not null,
    phone           text,
    email           text,
    device_id       text,
    invoice_id      text,
    dispatch_status text not null default 'Pending',
    created_at      timestamptz not null default now()
);
create index idx_customers_tenant on customers(tenant_id);

-- ============================================================
-- SUPPORT TICKETS
-- ============================================================
create table support_tickets (
    id          text primary key,
    tenant_id   text not null references tenants(id) on delete cascade,
    customer_id text not null references customers(id) on delete cascade,
    title       text not null,
    description text,
    status      text not null default 'Open', -- 'Open', 'Converted', 'Closed'
    task_id     text, -- populated once converted to task
    created_at  timestamptz not null default now()
);
create index idx_support_tickets_tenant on support_tickets(tenant_id);
create index idx_support_tickets_customer on support_tickets(customer_id);

-- ============================================================
-- INVENTORY
-- ============================================================
create table inventory_items (
    id            text primary key,
    tenant_id     text not null references tenants(id) on delete cascade,
    name          text not null,
    serial_number text not null,
    status        text not null default 'In_Stock',
    assigned_to   text references users(id),
    region        text not null,
    -- Added for the "robust inventory" pass — not in the original in-memory
    -- model, needed for reorder alerts.
    reorder_threshold int not null default 0,
    unit_cost numeric(14,2) not null default 0,
    created_at    timestamptz not null default now(),
    unique (tenant_id, serial_number)
);
create index idx_inventory_tenant on inventory_items(tenant_id);
create index idx_inventory_status on inventory_items(tenant_id, status);


-- ============================================================
-- MATERIAL REQUESTS & PROCUREMENT
-- ============================================================
create table material_requests (
    id           text primary key,
    tenant_id    text not null references tenants(id) on delete cascade,
    task_id      text,
    requester_id text not null references users(id),
    item_name    text not null,
    status       text not null default 'Started',
    allocated_sn text,
    created_at   timestamptz not null default now()
);
create index idx_material_requests_tenant on material_requests(tenant_id);
create table if not exists inventory_reservations (
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
create index if not exists idx_inventory_reservations_tenant on inventory_reservations(tenant_id, status);

create table procurement_orders (
    id            text primary key,
    tenant_id     text not null references tenants(id) on delete cascade,
    request_id    text references material_requests(id),
    item_name     text not null,
    expected_time timestamptz,
    status        text not null default 'Bidding',
    barcode_photo_url text,
    confirmed_at  timestamptz,
    confirmed_by  text
);
create index idx_procurement_tenant on procurement_orders(tenant_id);

-- ============================================================
-- FINANCE
-- ============================================================
create table invoices (
    id             text primary key,
    tenant_id      text not null references tenants(id) on delete cascade,
    customer_id    text references customers(id),
    total_amount   numeric(14,2) not null default 0,
    paid_amount    numeric(14,2) not null default 0,
    balance_amount numeric(14,2) not null default 0,
    status         text not null default 'Draft',
    region         text not null,
    created_at     timestamptz not null default now()
);
create index idx_invoices_tenant on invoices(tenant_id);

create table payments (
    id             text primary key,
    tenant_id      text not null references tenants(id) on delete cascade,
    invoice_id     text not null references invoices(id) on delete cascade,
    amount         numeric(14,2) not null,
    payment_method text not null,
    reference      text,
    paid_at        timestamptz not null default now()
);
create index idx_payments_tenant on payments(tenant_id);
create index idx_payments_invoice on payments(invoice_id);

-- ============================================================
-- TASKS & TIMESHEETS
-- ============================================================
create table tasks (
    id          text primary key,
    tenant_id   text not null references tenants(id) on delete cascade,
    title       text not null,
    assigned_to text references users(id),
    created_by  text not null references users(id),
    status      text not null default 'Pending',
    region      text not null,
    -- Added for the "robust task management" pass.
    due_date    date,
    depends_on  text references tasks(id),
    created_at  timestamptz not null default now()
);
create index idx_tasks_tenant on tasks(tenant_id);
create index idx_tasks_assignee on tasks(tenant_id, assigned_to);

create table timesheets (
    id          text primary key,
    tenant_id   text not null references tenants(id) on delete cascade,
    task_id     text not null references tasks(id) on delete cascade,
    user_id     text not null references users(id),
    hours       numeric(5,2) not null,
    worked_on   date not null,
    status      text not null default 'Submitted',
    approved_by text references users(id)
);
create index idx_timesheets_tenant on timesheets(tenant_id);

-- ============================================================
-- AUDIT LOG
-- ============================================================
create table erp_logs (
    id         bigint generated always as identity primary key,
    tenant_id  text not null,
    user_id    text not null,
    action     text not null,
    details    text,
    created_at timestamptz not null default now()
);
create index idx_erp_logs_tenant on erp_logs(tenant_id, id desc);

-- ============================================================
-- ROW LEVEL SECURITY — defense in depth behind the app-layer tenant filter
-- ============================================================
alter table roles enable row level security;
alter table roles force row level security;
alter table users enable row level security;
alter table users force row level security;
alter table customers enable row level security;
alter table customers force row level security;
alter table inventory_items enable row level security;
alter table inventory_items force row level security;
alter table inventory_reservations enable row level security;
alter table inventory_reservations force row level security;
alter table material_requests enable row level security;
alter table material_requests force row level security;
alter table procurement_orders enable row level security;
alter table procurement_orders force row level security;
alter table invoices enable row level security;
alter table invoices force row level security;
alter table payments enable row level security;
alter table payments force row level security;
alter table tasks enable row level security;
alter table tasks force row level security;
alter table timesheets enable row level security;
alter table timesheets force row level security;
alter table erp_logs enable row level security;
alter table erp_logs force row level security;
alter table invitations enable row level security;
alter table invitations force row level security;
alter table support_tickets enable row level security;
alter table support_tickets force row level security;

-- One policy per table, all following the same shape: only rows whose
-- tenant_id matches the session variable the Go app sets per request.
create policy tenant_isolation on roles             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on users             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on customers         using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on inventory_items    using (tenant_id = current_setting('app.current_tenant_id', true));
drop policy if exists inventory_reservation_tenant_isolation on inventory_reservations;
drop policy if exists tenant_isolation_inventory_reservations on inventory_reservations;
create policy inventory_reservation_tenant_isolation on inventory_reservations using (tenant_id = current_setting('app.current_tenant_id', true)) with check (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on material_requests  using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on procurement_orders using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on invoices          using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on payments          using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on tasks             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on timesheets        using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on erp_logs          using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on invitations       using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on support_tickets   using (tenant_id = current_setting('app.current_tenant_id', true));

-- The app connects with a single Postgres role (not per-tenant DB roles),
-- so grant that role bypass-free access and let RLS above do the filtering.
-- Replace `erp_app` with whatever role your Go service's DSN authenticates as.
--
-- WHY BYPASSRLS, NOT SUPERUSER (read this before changing it):
-- A handful of queries in db.go legitimately can't scope by tenant up front
-- — GetUserByEmail (login happens before we know which tenant a user is
-- in), the platform-wide email-uniqueness check in RegisterUserTransaction,
-- and a few "look up by opaque ID, then verify tenant" reads. Under RLS
-- with no session variable set, current_setting('app.current_tenant_id')
-- is NULL and `tenant_id = NULL` is never true — so these queries would
-- return zero rows unconditionally, which breaks login outright, not just
-- leaks data. That's a correctness bug, not a security feature.
--
-- The fix is BYPASSRLS specifically, not SUPERUSER (which the CI workflow
-- used to grant, and which is much more than this role needs — it also
-- allows creating/dropping roles and databases). BYPASSRLS only skips RLS
-- policy evaluation; every other Postgres privilege still applies normally,
-- and still only within whatever GRANTs you give it below.
--
-- This is safe here specifically because tenant isolation for every
-- write path is already enforced explicitly in Go, not left to RLS alone:
-- GetStateForTenant filters every SELECT with tenant_id = $1, and
-- consumer/consumer.go independently re-verifies the caller's tenant
-- before every mutation (search that file for CrossTenantInventoryAccess,
-- CrossTenantFinanceAccess, CrossTenantAttack) and raises a security alert
-- if it doesn't match. RLS remains enabled on every table below as a
-- second layer against a *different*, less-trusted credential ever
-- querying this database directly (e.g. the Supabase SQL editor logged in
-- as a lower-privileged role, or a future read-only reporting connection)
-- — it's just not the layer the app itself depends on to function.
--
-- One more subtlety worth being explicit about: every table below also has
-- FORCE ROW LEVEL SECURITY set (not just ENABLE). Without FORCE, Postgres
-- exempts the table's OWNER from RLS by default — and since the CI/setup
-- flow below has erp_app create the schema itself (so it owns every table
-- it creates), RLS would silently do nothing even without BYPASSRLS, for
-- reasons that have nothing to do with the BYPASSRLS grant at all. FORCE
-- makes the exemption explicit and intentional (via BYPASSRLS) rather than
-- an accident of who happened to run the migration.
--
-- create role erp_app with login password '...' bypassrls;
-- grant usage on schema public to erp_app;
-- grant select, insert, update, delete on all tables in schema public to erp_app;
-- grant usage, select on all sequences in schema public to erp_app;

-- ============================================================
-- INVENTORY HISTORY (Task 3)
-- ============================================================
create table inventory_history (
    id         bigint generated always as identity primary key,
    tenant_id  text not null references tenants(id) on delete cascade,
    item_id    text not null,
    from_status text not null,
    to_status   text not null,
    changed_by  text not null,
    changed_at  timestamptz not null default now()
);
create index idx_inventory_history_tenant on inventory_history(tenant_id);
alter table inventory_history enable row level security;
alter table inventory_history force row level security;
create policy tenant_isolation on inventory_history using (tenant_id = current_setting('app.current_tenant_id', true));

-- ============================================================
-- INVOICE NOTES & SERIALIZED REQUISITIONS (New Workflows)
-- ============================================================
create table invoice_notes (
    id           text primary key,
    tenant_id    text not null references tenants(id) on delete cascade,
    request_id   text not null references material_requests(id) on delete cascade,
    item_name    text not null,
    allocated_sn text not null,
    task_id      text,
    requester_id text not null references users(id),
    usage_type   text not null default 'Internal', -- 'Internal', 'Customer_Installation', 'Customer_Broken'
    status       text not null default 'Paid_Usage_Support', -- 'Paid_Usage_Support', 'Pending_Payment', 'Cleared_Paid', 'Reconciliation_Started'
    invoice_id   text references invoices(id) on delete set null,
    payment_id   text references payments(id) on delete set null,
    created_at   timestamptz not null default now()
);
create index idx_invoice_notes_tenant on invoice_notes(tenant_id);
alter table invoice_notes enable row level security;
alter table invoice_notes force row level security;
create policy tenant_isolation on invoice_notes using (tenant_id = current_setting('app.current_tenant_id', true));

-- ============================================================
-- BUSINESS TRANSACTION LEDGER / PROFITABILITY
-- ============================================================
create table if not exists business_ledger_entries (
    id text primary key,
    tenant_id text not null references tenants(id) on delete cascade,
    transaction_id text not null,
    task_id text references tasks(id) on delete set null,
    invoice_id text references invoices(id) on delete set null,
    entity_type text not null,
    entity_id text not null,
    account text not null,
    entry_type text not null,
    amount numeric(14,2) not null,
    quantity numeric(14,4) not null default 1,
    unit_cost numeric(14,2) not null default 0,
    currency char(3) not null default 'KES',
    reference text,
    actor_id text,
    reversal_of text references business_ledger_entries(id),
    created_at timestamptz not null default now(),
    unique (tenant_id, transaction_id, entry_type, account)
);
create index if not exists idx_ledger_tenant_task on business_ledger_entries(tenant_id, task_id, created_at desc);
create index if not exists idx_ledger_tenant_invoice on business_ledger_entries(tenant_id, invoice_id, created_at desc);
create index if not exists idx_ledger_transaction on business_ledger_entries(tenant_id, transaction_id);
alter table business_ledger_entries enable row level security;
alter table business_ledger_entries force row level security;
drop policy if exists ledger_tenant_isolation on business_ledger_entries;
create policy ledger_tenant_isolation on business_ledger_entries using (tenant_id = current_setting('app.current_tenant_id', true)) with check (tenant_id = current_setting('app.current_tenant_id', true));

create table if not exists labor_rates (
    id text primary key,
    tenant_id text not null references tenants(id) on delete cascade,
    user_id text references users(id) on delete cascade,
    role_name text,
    hourly_rate numeric(14,2) not null,
    currency char(3) not null default 'KES',
    active boolean not null default true,
    created_at timestamptz not null default now()
);
create index if not exists idx_labor_rates_lookup on labor_rates(tenant_id, user_id, role_name, active);
alter table labor_rates enable row level security;
alter table labor_rates force row level security;
drop policy if exists labor_rates_tenant_isolation on labor_rates;
create policy labor_rates_tenant_isolation on labor_rates using (tenant_id = current_setting('app.current_tenant_id', true)) with check (tenant_id = current_setting('app.current_tenant_id', true));

-- ============================================================
-- TRANSACTIONAL OUTBOX / CONSUMER INBOX
-- ============================================================
create table if not exists outbox_events (
    id text primary key,
    tenant_id text not null references tenants(id) on delete cascade,
    aggregate_type text not null,
    aggregate_id text not null,
    subject text not null,
    payload jsonb not null default '{}'::jsonb,
    headers jsonb not null default '{}'::jsonb,
    status text not null default 'PENDING',
    attempts integer not null default 0,
    next_attempt_at timestamptz not null default now(),
    published_at timestamptz,
    last_error text,
    created_at timestamptz not null default now()
);
create index if not exists idx_outbox_pending on outbox_events(status, next_attempt_at, created_at);
create index if not exists idx_outbox_tenant on outbox_events(tenant_id, created_at desc);

create table if not exists inbox_messages (
    message_id text primary key,
    consumer_name text not null,
    tenant_id text not null,
    subject text not null,
    status text not null default 'PROCESSING',
    attempts integer not null default 1,
    received_at timestamptz not null default now(),
    processed_at timestamptz,
    last_error text
);
create index if not exists idx_inbox_consumer_status on inbox_messages(consumer_name,status,received_at);
create index if not exists idx_inbox_tenant on inbox_messages(tenant_id,received_at desc);


-- ============================================================
-- V8 TRANSACTION ORCHESTRATION HARDENING
-- ============================================================
alter table payments add column if not exists workflow_id text;
create unique index if not exists uq_payment_reference_v8
    on payments(tenant_id, reference) where reference is not null and reference <> '';
alter table inbox_messages add column if not exists locked_at timestamptz;
alter table inbox_messages add column if not exists completed_at timestamptz;


alter table outbox_events enable row level security;
alter table outbox_events force row level security;
drop policy if exists outbox_tenant_isolation on outbox_events;
create policy outbox_tenant_isolation on outbox_events using (tenant_id = current_setting('app.current_tenant_id', true)) with check (tenant_id = current_setting('app.current_tenant_id', true));
alter table inbox_messages enable row level security;
alter table inbox_messages force row level security;
drop policy if exists inbox_tenant_isolation on inbox_messages;
create policy inbox_tenant_isolation on inbox_messages using (tenant_id = current_setting('app.current_tenant_id', true)) with check (tenant_id = current_setting('app.current_tenant_id', true));

-- ============================================================
-- FIELD SERVICE TELEMETRY (v13)
-- ============================================================
create table if not exists field_attendance (
 id text primary key,
 tenant_id text not null references tenants(id) on delete cascade,
 user_id text not null references users(id),
 clock_in_at timestamptz not null,
 clock_in_lat numeric(10,7) not null,
 clock_in_lon numeric(10,7) not null,
 clock_in_photo_url text not null,
 clock_out_at timestamptz,
 clock_out_lat numeric(10,7),
 clock_out_lon numeric(10,7),
 clock_out_photo_url text,
 status text not null default 'OPEN',
 created_at timestamptz not null default now()
);
create index if not exists idx_field_attendance_tenant_user on field_attendance(tenant_id,user_id,clock_in_at desc);
create unique index if not exists ux_field_attendance_open_user on field_attendance(tenant_id,user_id) where status='OPEN';
create table if not exists field_job_events (
 id text primary key,
 tenant_id text not null references tenants(id) on delete cascade,
 project_id text references projects(id) on delete cascade,
 task_id text references tasks(id) on delete cascade,
 technician_id text not null references users(id),
 event_type text not null,
 occurred_at timestamptz not null default now(),
 latitude numeric(10,7), longitude numeric(10,7),
 photo_url text, customer_signature_url text, note text,
 created_at timestamptz not null default now()
);
create index if not exists idx_field_job_events_tenant_time on field_job_events(tenant_id,occurred_at desc);
create index if not exists idx_field_job_events_task on field_job_events(tenant_id,task_id,occurred_at desc);
