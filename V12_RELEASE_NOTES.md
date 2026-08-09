# V12 Release Notes

- Added tenant-scoped Field-Service Intelligence endpoint.
- Added dashboard intelligence cards for workload, SLA risk, completion, margin and CSAT.
- Added technician workload panel.
- Added regional performance panel.
- Added material variance panel.
- Added customer voice panel.
- Reused existing PostgreSQL task, timesheet, business-ledger, project BOM and satisfaction records.
- Added no new fake operational data sources.
- Formatted Go sources with gofmt.
- All four embedded browser JavaScript blocks pass `node --check`.
- Full Go tests could not run because the repository requires Go 1.26.5 and the available toolchain is Go 1.23.2.
