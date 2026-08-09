# v6 — Governed Command Release

v6 closes the gap between the workflow ledger and the event bus.

## Control path

`REQUESTED -> APPROVED -> EXECUTED -> JetStream command -> RECONCILED`

A workflow stores:
- tenant
- entity type/id
- workflow key
- requester/checker/executor/reconciler
- immutable decision history
- approved internal NATS command subject
- original command payload

The command payload is held until an authorized `execute` decision. The workflow service then publishes it to JetStream. If publication fails, the workflow is returned to `APPROVED` so execution can be retried.

## Integrated flows

### Material request
1. Technician submits material request.
2. Server creates a `MATERIAL_REQUEST` workflow and assigns a request ID.
3. No NATS command is published yet.
4. Checker approves.
5. Authorized executor performs `execute`.
6. Only then is `erp.tasks.materials.cmd.request` published.
7. Consumer creates the material request using the workflow-generated request ID.

### Timesheet approval
1. Manager opens a timesheet for approval.
2. Server creates a `TIMESHEET` workflow.
3. No approval command is published yet.
4. Checker approves.
5. Executor releases `erp.tasks.timesheet.cmd.approve`.

## Safety controls

- JWT authentication remains mandatory.
- Workflow command subjects are allow-listed server-side.
- Tenant ID comes from verified JWT context.
- Requester cannot approve/execute their own workflow.
- Workflow records have tenant RLS policies.
- Active duplicate workflows for the same tenant/entity are prevented.
- Compatibility approval endpoints no longer publish business commands directly.

## Important production hardening still recommended

The next step is a transactional **outbox/inbox pattern**. The current v6 release persists `EXECUTED`, publishes to JetStream, and rolls back to `APPROVED` if publishing fails. This is safe against normal publish failures, but a process crash exactly between the database commit and NATS publish can still require reconciliation.

The outbox should therefore atomically record:
`workflow decision + command release intent + audit event`,
then a durable publisher should deliver the command and mark the outbox row as published.
