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
    created_at    timestamptz not null default now(),
    foreign key (tenant_id, role_name) references roles(tenant_id, name)
);
create index idx_users_tenant on users(tenant_id);
create index idx_users_email on users(lower(email));

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

create table procurement_orders (
    id            text primary key,
    tenant_id     text not null references tenants(id) on delete cascade,
    request_id    text references material_requests(id),
    item_name     text not null,
    expected_time timestamptz,
    status        text not null default 'Bidding'
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
alter table users enable row level security;
alter table customers enable row level security;
alter table inventory_items enable row level security;
alter table material_requests enable row level security;
alter table procurement_orders enable row level security;
alter table invoices enable row level security;
alter table payments enable row level security;
alter table tasks enable row level security;
alter table timesheets enable row level security;
alter table erp_logs enable row level security;

-- One policy per table, all following the same shape: only rows whose
-- tenant_id matches the session variable the Go app sets per request.
create policy tenant_isolation on roles             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on users             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on customers         using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on inventory_items    using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on material_requests  using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on procurement_orders using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on invoices          using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on payments          using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on tasks             using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on timesheets        using (tenant_id = current_setting('app.current_tenant_id', true));
create policy tenant_isolation on erp_logs          using (tenant_id = current_setting('app.current_tenant_id', true));

-- The app connects with a single Postgres role (not per-tenant DB roles),
-- so grant that role bypass-free access and let RLS above do the filtering.
-- Replace `erp_app` with whatever role your Go service's DSN authenticates as.
-- create role erp_app login password '...';
-- grant usage on schema public to erp_app;
-- grant select, insert, update, delete on all tables in schema public to erp_app;

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
create policy tenant_isolation on inventory_history using (tenant_id = current_setting('app.current_tenant_id', true));
