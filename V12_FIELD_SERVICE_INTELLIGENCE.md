# V12 — Field-Service Intelligence

## Purpose
Turn existing ERP execution, finance and customer records into operational intelligence without inventing data that the current schema does not persist.

## Management chain
Technician workload -> SLA exposure -> regional delivery -> material variance -> gross margin -> customer voice.

## Metrics
- Open, overdue and 24-hour SLA-risk jobs.
- Completion rate.
- Active technician count.
- Revenue, material cost, labour cost and gross margin from the business ledger.
- Average customer satisfaction and response count.
- Technician workload and approved labour hours.
- Regional completion and overdue rates.
- Project planned-versus-actual material variance.
- Customer-level satisfaction ranking.

## API
`GET /api/intelligence`

Requires `tasks:read` and uses the authenticated tenant context.

## Deliberate limitations
Attendance, GPS route efficiency, first-time-fix and job cycle time are not fabricated in v12 because the current database does not contain reliable source events for those measures. They should be added in a later field-service telemetry/attendance module.
