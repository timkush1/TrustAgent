"""
Runtime configuration of the engine (SPEC section 3.2 `runtime/`, P4-PLAN D34, D42, D78).

- `state.RuntimeState`: which (epoch, version) push is applied.
- `manager.ProviderManager`: which judge runs, from the environment or a push.
- `origins`: where a pushed credential may be sent.
- `redaction`: secrets never reach a log line.
"""
