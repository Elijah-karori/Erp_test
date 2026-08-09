# V13 Release Notes

## Added
- Field attendance clock-in/out with GPS and photo reference.
- One-open-attendance-session guard per tenant/user.
- Field job telemetry events: arrival, departure, first-time-fix, revisit-required, customer-signed and job note.
- Tenant RLS for telemetry tables.
- Field telemetry API and Command Center UI.
- Current GPS capture in browser using the Geolocation API.
- Management KPIs for active clock-ins, daily clock-ins, first-time-fix, revisits and signatures.
- Auditable recent field-event feed.

## Validation
- `gofmt` completed.
- All 6 embedded browser JavaScript blocks pass `node --check`.
- Full `go test ./...` could not execute because the repository requires Go 1.26.5 and the environment cannot download the toolchain due to blocked DNS/network access.
