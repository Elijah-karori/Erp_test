# ERP Event Bus Rules

This codebase implements context propagation, event taxonomy, stream topology, and consumer designs for an ERP Event Bus using Go/Echo and NATS JetStream.

## 1. NATS Acknowledgment Rules
- **Terminal Errors:** For any RBAC/ABAC authorization failure, missing HTTP/NATS identity headers, or malformed JSON payloads, you MUST use `msg.Term()`. Do NOT use `msg.Nak()`. This prevents infinite validation loops.
- **Transient Errors:** `msg.Nak()` is strictly reserved for infrastructure failures (e.g. database timeouts, lock contention, network disconnects) where a retry might succeed on the next delivery.

## 2. Identity Propagation & Context Mapping
- Echo HTTP Handlers must never process complex resource-level business authorization directly.
- They must validate synchronous Edge RBAC (checking globally assigned roles), map the user's details (`X-User-Id`, `X-User-Roles`, `X-User-Region`), and publish a NATS message with these metadata fields injected into `msg.Header` before hitting the bus.

## 3. Worker Enforcement
- Consumers (workers) are the final line of defense and perform asynchronous Attribute-Based Access Control (ABAC) checks.
- If a consumer fails an ABAC check (e.g., region mismatch), it must audit-trail the violation by publishing a security alert to `erp.security.audit.v1.failed_access` before calling `msg.Term()`.
