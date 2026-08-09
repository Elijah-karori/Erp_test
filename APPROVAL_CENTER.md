# Governance & Approval Center

The ERP now exposes a unified approval surface at **Approval Center**.

## Control lifecycle

1. Requested — maker creates a material requisition, timesheet, procurement order, or billing usage record.
2. Checked — an authorized checker sees the record in the central queue.
3. Executed — the existing command is dispatched through the Go/NATS JetStream path.
4. Reconciled — receipt/payment/evidence state remains visible from the queue and existing module workspaces.

## Existing backend controls retained

- Tenant isolation is still enforced by JWT context and database tenant scoping.
- Approval actions continue to use the existing permission middleware.
- Material approval uses `tasks:approve`.
- Timesheet approval uses `timesheets:approve`.
- Procurement receipt confirmation uses `inventory:write`.
- Billing reconciliation opens the existing task/requisition reconciliation workspace and uses `tasks:write` where applicable.
- No client-side permission is treated as authoritative; the backend remains the enforcement point.

## Validation

- `gofmt` completed on the Go source.
- Browser JavaScript extracted from `htmlContent` passes `node --check`.
- Full Go tests could not be run in this environment because the repository requires Go 1.26.5 while the available local toolchain is Go 1.23.2 and the environment cannot download the required toolchain.
