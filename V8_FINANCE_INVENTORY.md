# V8 Finance + Inventory Transaction Orchestration

## Material lifecycle

Technician request -> governance workflow -> approval -> execution -> transactional outbox -> JetStream -> consumer inbox -> serialized inventory allocation OR procurement escalation -> invoice usage note -> audit.

The request ID is the idempotency key. Inventory selection uses `FOR UPDATE SKIP LOCKED`, preventing two concurrent fulfillment workers from allocating the same serialized unit.

The old direct `materials.cmd.approve` path is no longer part of the governed workflow. Approval changes workflow state; execution releases `materials.cmd.fulfill` through the outbox.

## Payment lifecycle

Payment command -> tenant/reference idempotency check -> payment insert -> invoice balance transaction -> payment reconciliation.

A redelivered payment with the same tenant/reference is acknowledged without applying the amount twice. A database unique index is the final race-condition guard.

## Delivery guarantees

The architecture remains at-least-once. PostgreSQL outbox creation is committed with the workflow execution. JetStream `Nats-Msg-Id` provides stable message identity. The consumer inbox suppresses completed duplicates and allows abandoned processing locks to be reclaimed after five minutes.

## Control boundary

The workflow engine governs whether a command may be released. The business transaction remains responsible for inventory/finance invariants. The consumer must never trust a UI status as proof of authorization.
