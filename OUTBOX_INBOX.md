# v7 — Transactional Outbox + Consumer Inbox

## Delivery contract

Business state and outbound command intent are committed in PostgreSQL together. A background publisher then delivers the outbox event to NATS JetStream with `Nats-Msg-Id=<outbox id>`.

If the process dies after NATS accepts the message but before PostgreSQL marks the outbox row as published, the same outbox event can be retried. JetStream/NATS de-duplication uses the stable message ID, while consumers also persist the delivery in `inbox_messages`.

## Workflow execution

`APPROVED -> EXECUTED + outbox row` is one PostgreSQL transaction. There is no direct NATS publish in the HTTP workflow decision path.

## Consumer idempotency

Each durable consumer claims a message in `inbox_messages` before applying business logic and marks it `PROCESSED` only after the business operation succeeds. A redelivery of a processed message is acknowledged without repeating the operation.

The inbox is an at-least-once/idempotency layer, not a claim of distributed exactly-once execution. Business handlers should continue to use natural idempotency keys/unique constraints for side effects such as payment references and serialized inventory identifiers.

## Retry behavior

Outbox failures use exponential backoff capped at five minutes. Unpublished rows remain durable until successfully delivered.
