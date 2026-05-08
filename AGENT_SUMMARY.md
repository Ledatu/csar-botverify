# csar-botverify Agent Summary

## Role In Prod
Webhook bridge for bot verification. It receives provider callbacks, extracts verification codes, and relays a confirm call to `csar-authn` through the csar router using STS service tokens.

## Runtime Entry Points
- `cmd/csar-botverify/main.go` loads config, registers providers, starts webhook handling, and runs the HTTP server.
- `internal/relay/relay.go` handles inbound webhook dispatch and outbound confirmation calls.
- `internal/provider/telegram/telegram.go` implements the Telegram webhook provider and webhook registration.

## Trust Boundary
- Webhook authenticity is provider-specific, not router-authenticated. Telegram relies on its webhook secret and the service-level verification logic.
- The confirm call goes through `stsclient.NewRouterClient`, so outbound identity is router-mediated rather than direct authn access.
- Inbound HTTP still uses `gatewayctx.Middleware`, but the public webhook route is intentionally outside the normal session/authn path.

## Public And Internal Surfaces
- Public surface: `POST /svc/botverify/webhook/{provider}` in router config.
- Internal surface: `POST /svc/authn/bot-verify/confirm` to authn through the router with STS and throttling.
- Health is exposed either inline or via the sidecar depending on config.

## Dependencies
- `csar-core` for config loading, gateway context, STS router client, HTTP helpers, TLS, health, and tracing.
- `csar-authn` as the confirmation target.
- Provider-specific webhook secrets and outbound provider APIs.

## Audit Hotspots
- The webhook route is public and has no explicit router throttling in prod config.
- Telegram webhook registration uses synchronous startup HTTP calls, so a slow upstream can block startup.
- Outbound HTTP clients should always have explicit timeouts.
- The dirty `cmd/csar-botverify/main.go` sidecar bind uses `:%d`; if kept,
  treat health/readiness as all-interface listeners and constrain them with
  compose/network policy rather than assuming loopback-only exposure.

## First Files To Read
- `cmd/csar-botverify/main.go`
- `internal/config/config.go`
- `internal/relay/relay.go`
- `internal/provider/telegram/telegram.go`
- `csar-configs/prod/csar/botverify-svc/routes.yaml`
- `csar-configs/prod/csar/authn-botverify-svc/routes.yaml`

## DRY / Extraction Candidates
- Keep the router-backed STS client pattern aligned with `csar-core/stsclient`.
- If more bot providers appear, factor shared webhook validation and outbound timeout defaults before copying logic into new provider packages.

## Required Quality Gates
- `go build ./...`
- `go test ./... -count=1`
- `golangci-lint run ./...`
