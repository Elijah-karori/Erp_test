# v10 — Customer / Project Lifecycle

## Business chain

Lead → Customer → Quote → Approval → Project/Job → Technician → Materials → Labour → Completion Evidence → Invoice → Payment → Profitability → Customer Satisfaction

## Data model

- `leads`: acquisition source, owner, pipeline status.
- `customers`: converted commercial party and existing CRM record.
- `quotes`: commercial value, validity, maker/checker state.
- `projects`: delivery container tied to customer and optionally an approved quote.
- `project_tasks`: links existing field tasks to a project.
- `project_evidence`: immutable-ish completion evidence records such as signatures, photos and delivery notes.
- `customer_satisfaction`: post-delivery customer voice.
- `tasks.customer_id/project_id`: operational tasks can now carry customer/project context.
- `invoices.project_id`: finance can associate an invoice with the delivery project.

## Controls

- All lifecycle tables are tenant-scoped and protected by PostgreSQL RLS.
- Lead conversion is transactional: customer creation and lead status change commit together.
- A project linked to a quote requires the quote to be `Approved`.
- Quote approval rejects self-approval by the quote creator.
- Existing task RBAC is reused; field technicians receive `tasks:write` so they can participate in execution evidence and project linkage.

## API

- `GET /api/lifecycle`
- `POST /api/lifecycle/leads`
- `POST /api/lifecycle/leads/convert`
- `POST /api/lifecycle/quotes`
- `POST /api/lifecycle/quotes/submit`
- `POST /api/lifecycle/quotes/approve`
- `POST /api/lifecycle/projects`
- `POST /api/lifecycle/projects/task`
- `POST /api/lifecycle/evidence`
- `POST /api/lifecycle/satisfaction`

## UI

The Customer / Project Lifecycle workspace provides:

- pipeline counters
- lead capture and conversion
- quote creation and approval actions
- project launch from an approved quote
- completion evidence capture
- customer satisfaction capture
- customer/project-aware operational context

## Deliberate next step

Invoice/project association should be exposed as a controlled finance action, followed by customer communication automation and project-level profitability aggregation. The existing ledger remains the financial source of truth.
