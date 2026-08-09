# v9 — Business Transaction Ledger & Job Profitability

## Objective
Turn operational activity into an auditable economic chain: job → material → labour → customer charge → payment → margin.

## Ledger contract
`business_ledger_entries` is append-only at the application layer. Corrections use compensating reversal entries rather than editing historical entries. Every row is tenant-scoped and carries a stable `transaction_id`.

Accounts currently emitted:
- `MATERIAL_COST` — debit when a serialized inventory unit is issued to a material request.
- `LABOR_COST` — debit when a timesheet is approved, using the configured user/role hourly rate.
- `REVENUE` — credit when a customer-facing invoice is generated from an invoice note.
- `ACCOUNTS_RECEIVABLE` — credit when an invoice payment is applied.

## Profitability
The `/api/profitability` endpoint groups ledger entries by task and reports revenue, material cost, labour cost, gross margin and margin percentage.

## APIs
- `GET /api/ledger` — tenant-scoped ledger; optional `task_id` filter.
- `GET /api/profitability` — job profitability summary.
- `POST /api/ledger/reverse` — finance-controlled compensating reversal; requires `finance:write`.

## Cost configuration
`inventory_items.unit_cost` stores the cost snapshot used for serialized material issuance. `labor_rates` supports a user-specific rate first, then a role fallback. Missing labour rates result in zero labour cost until a rate is configured.

## Reliability
Ledger writes occur inside the same PostgreSQL transaction as the business operation that caused them. This prevents a successful material issue, timesheet approval or customer invoice creation from committing without its corresponding financial trace.

## Important accounting boundary
This is a management/profitability ledger, not a full double-entry general ledger yet. The next finance phase should introduce a chart of accounts, journal batches, balanced debit/credit validation, tax/VAT treatment, period closing and formal reconciliation.
