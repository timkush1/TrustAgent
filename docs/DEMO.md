# Live Demo Runbook

How to run TrustAgent locally and (optionally) expose it on a free public HTTPS
URL with Cloudflare Tunnel — e.g. to walk an interviewer through the live
dashboard. Total cost: $0.

## 1. Start the stack (local)

```bash
# one-time: .env with at least GRAFANA_ADMIN_PASSWORD set (see .env.example)
docker compose up -d redis postgres qdrant ollama prometheus grafana
docker exec truthtable-ollama ollama pull llama3.2:1b   # one-time, ~1.3 GB

# seed the knowledge base (one-time, idempotent)
cd backend-python && .venv/Scripts/python scripts/seed_knowledge.py

# three terminals (or run the full container stack: docker compose up -d --build)
make dev-python   # audit engine  (gRPC :50051)
make dev-go       # proxy         (HTTP/WS :8080)
make dev-react    # dashboard     (:5173)
```

> The engine caches its retrieval index in-process: if you re-seed the
> knowledge base while the engine is running, restart the engine (uploads via
> `/api/upload` refresh the index automatically; direct seeding does not).

Sanity check: `python test_e2e.py` from the repo root, then open
<http://localhost:5173> and watch audits stream in live.

### Demo script (90 seconds)

1. Open the dashboard. Submit a truthful response through the proxy:

   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -d '{"model":"demo","messages":[{"role":"user","content":"What is the capital of France?"}],"test_response":"Paris is the capital of France."}'
   ```

   → response returns instantly; seconds later the audit card appears with a
   high score (Grade A, SUPPORTED claim + evidence).

2. Submit a hallucination: same request with
   `"test_response":"London is the capital of France."`
   → Grade D, hallucination flag, UNSUPPORTED claim with the contradicting
   evidence shown.

3. Show the Knowledge Base view (accepted claims, conflicts) and Grafana
   (`http://localhost:3001`) for the ops story.

## 2. Expose it publicly (free, on demand)

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
gives a public HTTPS URL that forwards to localhost. Quick tunnels need no
account; the URL is random and lives as long as the process.

```bash
winget install Cloudflare.cloudflared   # or download from cloudflare.com

# terminal A: expose the dashboard
cloudflared tunnel --url http://localhost:5173
# prints e.g. https://random-words.trycloudflare.com

# terminal B: expose the proxy API/WebSocket
cloudflared tunnel --url http://localhost:8080
```

Then point the dashboard at the tunneled API before starting Vite:

```bash
# frontend-react/.env.local
VITE_API_URL=https://<proxy-tunnel>.trycloudflare.com
VITE_WS_URL=wss://<proxy-tunnel>.trycloudflare.com/ws
```

**Harden before exposing** (the proxy ships dev-open defaults):

```bash
# .env / shell env for the proxy
TRUTHTABLE_API_KEYS=<openssl rand -hex 32>          # turns auth on
TRUTHTABLE_ALLOWED_ORIGINS=https://<dashboard-tunnel>.trycloudflare.com
```

Keep Grafana, Prometheus, and the metrics ports un-tunneled. Stop the
`cloudflared` processes when the demo is over — nothing stays published.

> For a stable hostname across demos, create a free Cloudflare account and a
> named tunnel (`cloudflared tunnel create trustagent`), which also survives
> restarts.

## Known environment caveats

- **TLS-intercepting networks** (some corporate/VPN/antivirus setups) break
  downloads *inside* containers and tools that ship their own CA bundle:
  `ollama pull`, Docker image builds (apk/pip/npm), HuggingFace model fetches,
  and git-over-HTTPS. Symptoms: `certificate signed by unknown authority` /
  `unable to get local issuer certificate`. Fixes: run those steps on a
  normal network, or for git on Windows: `git config --global http.sslBackend
  schannel`. Cached models/images keep working offline
  (`HF_HUB_OFFLINE=1` forces the embedding model to use its local cache).
- **Judge model ceiling**: `llama3.2:1b` reliably separates clearly true vs.
  false claims but can misjudge subtle ones (see Limitations in the README).
  `llama3.2:3b` is a noticeably better free judge if you have ~4 GB RAM to
  spare: `docker exec truthtable-ollama ollama pull llama3.2:3b` and set
  `LLM_MODEL=llama3.2:3b`.
