# Governance Workflow Engine

v5 introduces a first-class maker-checker workflow ledger.

## Lifecycle
`REQUESTED -> APPROVED -> EXECUTED -> RECONCILED`

Alternative control paths: `RETURNED` and `REJECTED`.

## API
- `GET /api/workflows` — tenant-scoped workflow ledger
- `POST /api/workflows` — create a workflow instance
- `POST /api/workflows/decision` — approve/reject/return/execute/reconcile

## Controls
- Tenant scoped records
- Permission middleware on every endpoint
- Requester cannot approve or execute their own workflow
- Explicit transition validation
- Every decision writes an immutable `workflow_decisions` audit row

## Next integration
Existing material, timesheet, procurement and finance commands should create workflow instances when submitted. The workflow engine should then publish the corresponding NATS command only after the required transition is satisfied.
