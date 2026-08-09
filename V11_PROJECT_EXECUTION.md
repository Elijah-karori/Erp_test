# v11 — Project Execution & Customer Delivery

v11 turns the Customer/Project lifecycle into an executable job packet while retaining the existing task, inventory, workflow, outbox/inbox and ledger engines.

## Lifecycle

Customer → approved quote → project → technician assignment → schedule → BOM/material request → task execution → completion evidence → independent completion review → project completed → invoice/payment → profitability → customer satisfaction.

## New control tables

- `project_assignments`: tenant/project/task/technician ownership.
- `project_schedule`: scheduled field work windows.
- `project_bom`: planned material requirements with unit-cost snapshots.
- `completion_reviews`: independent completion gate and segregation-of-duties control.

## Completion gate

A project completion review requires:

1. At least one completion evidence record.
2. All linked project tasks to be `Completed`.
3. The completion reviewer to be different from the submitter.
4. `Approved` review changes the project status to `Completed`.
5. `Returned` preserves the review history and allows resubmission.

## APIs

- `GET /api/execution`
- `POST /api/execution/assign`
- `POST /api/execution/schedule`
- `POST /api/execution/bom`
- `POST /api/execution/complete/submit`
- `POST /api/execution/complete/review`

All endpoints are tenant-scoped and permission-gated.
