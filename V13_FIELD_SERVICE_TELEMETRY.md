# V13 — Field-Service Telemetry

V13 turns the field-service layer from reporting into auditable execution telemetry.

## Business chain

Technician attendance → job arrival/departure → first-time-fix/revisit → customer signature → completion → labour/material cost → profitability.

## Attendance control

`field_attendance` records clock-in/out. Clock-in requires GPS coordinates and a photo/object reference. A tenant/user can have only one open attendance session.

## Job events

`field_job_events` records ARRIVAL, DEPARTURE, FIRST_TIME_FIX, REVISIT_REQUIRED, CUSTOMER_SIGNED and JOB_NOTE events. Job events do not require GPS because the execution policy can permit task work without continuous location tracking; location is optional telemetry.

## Security

All records are tenant-scoped with PostgreSQL RLS. Attendance writes use the existing `timesheets:submit` permission. Job events use `tasks:write`. Reads use `tasks:read`.

## API

- `GET /api/field/telemetry`
- `POST /api/field/attendance`
- `POST /api/field/job-event`

## Reliability note

Telemetry is intentionally stored as immutable event records rather than overwriting task state. This permits later derivation of first-time-fix, revisit rate, arrival/departure duration, SLA exposure and customer-signoff compliance.

## Photo storage

V13 stores a photo/object reference (`photo_url`) rather than binary media in PostgreSQL. The next storage integration should use an object store and signed upload URLs.
