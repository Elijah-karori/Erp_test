# Implementation prompt: Supabase persistence + remaining hardening

Paste this into a Claude Code session (or any coding agent) with this repo
checked out and a real Go 1.26 toolchain + Supabase project available. It
assumes the auth work (JWT, RBAC gating, tenant-scoped `/api/state` and
`/api/logs`) already landed — read `middleware/middleware.go`,
`handler/auth_handler.go`, and `db/db.go` before touching anything, so you're
building on what's actually there instead of what you'd expect to find.

Work through the tasks in order. Each is a checkpoint — get it compiling and
passing `go vet ./...` before starting the next one. Don't batch all four
into one giant diff.

## Ground rules

- Every exported method on `*db.Database` and `*db.SQLiteDB` keeps its exact
  name and signature wherever a Postgres-backed version has a clean
  equivalent. Handlers should not need to change beyond the wiring in
  `main.go` — the goal is a drop-in storage swap, not a handler rewrite.
- Never trust a client-supplied `tenant_id` for anything security-relevant.
  It comes from `c.Get(middleware.ContextTenantID)` (set by
  `JWTAuthMiddleware` from the verified token), full stop.
- `supabase/schema.sql` in this repo is the target schema. If you need to
  change it, change the file and regenerate — don't let the live DB drift
  from what's checked in.
- Use `pgx` (`github.com/jackc/pgx/v5`) with a connection pool
  (`pgxpool.Pool`), not `database/sql` + a generic driver — Supabase's
  connection string is standard Postgres, and pgx's native protocol support
  is meaningfully faster for this.

## Task 1 — Postgres-backed persistence layer

Replace the in-memory maps in `db/db.go` with queries against the schema in
`supabase/schema.sql`, without changing the method signatures other code
depends on.

1. Add `DATABASE_URL` (or `SUPABASE_DB_URL`) as a required env var, read in
   `main.go` at startup. Fail fast with a clear error if it's missing —
   don't silently fall back to in-memory.
2. Run `supabase/schema.sql` against the project (Supabase SQL editor, or
   `psql "$DATABASE_URL" -f supabase/schema.sql`).
3. Replace `*Database`'s internal `sync.RWMutex` + map fields with a
   `*pgxpool.Pool`. Every method (`GetUser`, `GetUserByEmail`, `CreateUser`,
   `RegisterUserTransaction`, `ResetPasswordTransaction`,
   `GetStateForTenant`, `CreateInventoryItem`, `AssignDevice`, etc.) becomes
   a real query instead of a map read/write.
4. **Every query that touches a tenant-owned table must do two things**,
   not just one:
   - Filter with `WHERE tenant_id = $1` in the SQL itself (this is the real
     boundary — RLS is the backstop, not the primary control).
   - Before running the query, `SET LOCAL app.current_tenant_id = $1` on
     the transaction (inside a `pgx.Tx`, so it's reset automatically after
     commit/rollback) so the RLS policies in `schema.sql` actually engage.
     A query that skips this still gets RLS-blocked instead of silently
     returning cross-tenant rows — that's the point of having both layers.
5. `GetStateForTenant` becomes N parallel `SELECT ... WHERE tenant_id = $1`
   queries (one per collection) instead of N map filters. Run them
   concurrently with `errgroup` rather than sequentially — this endpoint is
   polled every 5s by the frontend, so latency compounds.
6. `NewDatabase`'s seed data becomes a one-time migration/seed script
   (`supabase/seed.sql` or a `--seed` flag on the binary), not something
   that runs on every boot. Guard it so it's a no-op if `tenants` already
   has rows.
7. `SQLiteDB.Log` / `GetLogsForTenant` move to the `erp_logs` table in the
   same Postgres database — drop the separate SQLite file entirely rather
   than running two databases side by side. Update `ExportLogsExcel`
   accordingly; the Excel generation logic itself doesn't need to change,
   only where the rows come from.

**Verify**: `go build ./...` clean, then walk through register → login →
create inventory item → assign it → check it shows up in `/api/state` for
that tenant and NOT for a second tenant you register separately.

## Task 2 — Tenant-scoped roles (closes the last cross-tenant escalation path)

`schema.sql` already keys `roles` by `(tenant_id, name)` instead of just
`name` — the schema is ready, the Go code isn't yet.

1. `db.CheckPermission(tenantID, roleName, permission string) bool` — add
   `tenantID` as a parameter everywhere this is called
   (`middleware.ModuleClearanceMiddleware` is the main caller).
2. `db.IsSubordinate` — same, needs `tenantID` to look up the right role
   hierarchy.
3. `db.GetRoles()` becomes `db.GetRolesForTenant(tenantID string)` —
   update `handler/ui.go`'s `GetState` to call it with the caller's own
   tenant instead of the global set.
4. Seed data: when a new tenant is created (either via
   `RegisterUserTransaction` bootstrapping a new org, or an eventual
   platform-admin tenant-creation flow), insert that tenant's own copy of
   the default role set (`tenant_admin`, `manager`, `finance_officer`,
   `field_technician` with the same default permissions defined today in
   `db.go`'s seed block) — don't reference a shared global row.
5. `UpdateRBAC`'s handler already reads `tenantID` from context (fixed in
   the auth pass) — thread it through to `UpdateRolePermissions(tenantID,
   roleName, permissions)` so it only ever touches that tenant's copy of
   the role.

**Verify**: register two separate tenants, change one's `manager` role
permissions via `/api/rbac/update`, confirm the other tenant's `manager`
role is untouched.

## Task 3 — Inventory hardening

1. Add `reorder_threshold int` to `InventoryItem` (already in
   `schema.sql`) — settable at creation, editable by `inventory:*`.
2. A background check (simplest: run on every `AssignDeviceHandler` and
   `CreateInventoryItemHandler` call, no need for a cron yet) that counts
   `In_Stock` items per `(tenant_id, name)` and, if the count drops to or
   below `reorder_threshold`, auto-creates a `procurement_orders` row with
   status `Bidding` if one doesn't already exist for that item name. This
   reuses the existing procurement flow — don't invent a second one.
3. Full audit history: every status transition on an `InventoryItem`
   (`In_Stock` → `Assigned` → `Deployed`, etc.) should append a row to a
   new `inventory_history` table (add to `schema.sql`:
   `id, tenant_id, item_id, from_status, to_status, changed_by, changed_at`)
   rather than only being visible as the item's current state. This is
   what makes "where did serial number X go" answerable later.
4. Serial number uniqueness is already enforced at the DB level
   (`unique (tenant_id, serial_number)` in `schema.sql`) — make sure
   `CreateInventoryItemHandler` surfaces that constraint violation as a
   clean 409, not a raw Postgres error.

## Task 4 — Task management hardening

1. `due_date` and `depends_on` are already in `schema.sql`. Wire them
   through `CreateItemCommand`-equivalent for tasks (there isn't one yet —
   add a `CreateTaskCommand` alongside the existing NATS command types in
   `types/types.go`, following the same pattern as `AssignDeviceCommand`).
2. A task with a `depends_on` set cannot transition to `In_Progress` until
   the task it depends on is `Completed` or `Approved` — enforce this in
   the consumer that handles task status transitions
   (`consumer/consumer.go`), not just in the frontend.
3. Overdue tasks (`due_date < now()` and `status` not in
   `Completed`/`Approved`) should be visible as a distinct filtered view
   in `/api/state` or a new `/api/tasks/overdue` endpoint — the frontend
   dashboard should surface these prominently rather than mixing them
   into the general task list silently.
4. `ApproveTimesheetHandler` already checks `timesheets:approve` — extend
   it (or add a sibling check) so a manager can only approve timesheets
   for tasks in their own tenant and region, not just their own tenant.
   `Task.Region` and `User.Region` already exist for this.

## Env vars this introduces

| Var | Required | Purpose |
|---|---|---|
| `DATABASE_URL` | yes | Supabase Postgres connection string (use the pooler URL, port 6543, for the app; direct 5432 connection only for running migrations) |
| `ERP_JWT_SECRET` | yes (from the auth pass) | JWT signing key |

## Acceptance checklist before calling this done

- [ ] `go build ./... && go vet ./...` clean
- [ ] Two tenants registered independently; no query or `/api/*` response
      returns data for the tenant you're not authenticated as
- [ ] Restarting the process does not lose data (the actual point of this
      task — confirm by creating an inventory item, restarting, and
      checking it's still there)
- [ ] `roles` permission changes for one tenant don't affect another
- [ ] Existing `test/integration_test.go` still passes (update it to spin
      up against a test Postgres schema/transaction-per-test instead of
      the in-memory `db.Database`, rather than deleting coverage)
