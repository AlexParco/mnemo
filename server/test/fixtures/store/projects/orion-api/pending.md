# Pending — Orion API

## In progress
- [ ] Finish the token rotation rollout [@fixture-box]
- [ ] Migrate 🐛 the legacy exporter the cursor pagination rewrite touches every list endpoint and the client SDK too before the freeze window closes

## Next
- [ ] Backfill the audit table [@other-box]
- [ ] Document the cursor contract
- [ ] Drop the v1 rate limiter [@ fixture-box ]
- [ ] Wire the SDK regression suite
- [x] Publish the 2.3 changelog

## Blocked
- [ ] Enable the new WAF rules — waiting on infra

## Debt
- [ ] The workers retry loop swallows errors
- [x] Removed the duplicated pagination helper

## Deployed
- [x] Token rotation behind a flag
- [x] Structured logs on the workers

## Cosas raras
- [ ] The staging clock drifts on Mondays
