# The demo

`make demo` runs all of TrustAgent on your machine with **no keys and no cost**: the fast check
(Jev) and the deep-audit judge are mocks, and a traffic generator replays scripted scenarios, so
the dashboard fills up by itself. Every number it shows is **simulated** and labelled that way.

This page covers running the demo, what to show in a few minutes, checking it from the terminal,
and sharing it. First-time setup (Docker, WSL) is in [GETTING-STARTED.md](GETTING-STARTED.md);
the gateway's behaviour is in [GATEWAY.md](GATEWAY.md).

## 1. Start it

```bash
make demo          # OPEN=0 prints the URLs without opening a browser
```

1. `scripts/configure.py --ensure` creates `.env` from `.env.example` and fills in the generated
   passwords (never printed).
2. The images are built from your checkout. The first run takes about 5 to 15 minutes
   **[estimate]**; later runs reuse the build cache.
3. The demo stack starts as its own Docker project, `truthtable-demo`: proxy, engine, Postgres,
   dashboard and the traffic generator. `docker-compose.demo.yml` removes every vendor key from
   the containers, whatever your `.env` holds, and turns on the mocks, test mode and a simulated
   calibration.
4. When every service is healthy, `make demo` mints a **one-time setup link** in the proxy and
   opens it:

   ```
   Setup (one-time link: it creates your admin key): http://localhost:3000/app/setup#token=…
   ```

   Click **Create the admin key**: the dashboard shows the key once (keep it) and signs this
   browser in, so you can edit policies and use the Playground. When an admin already exists,
   `make demo` opens the dashboard at <http://localhost:3000> instead.

Other ways in: `make demo-setup-url` prints a new link (each one replaces the last);
`TRUTHTABLE_ADMIN_KEYS=demo-admin=<a long random string>` in `.env` gives you a fixed admin key to
type at `/app/setup` (restart with `make demo`). Reading traffic needs no sign-in in the demo.

```bash
make demo-logs SERVICE=proxy   # follow one service (leave out SERVICE= for all)
make demo-down                 # stop; the demo data is kept
make demo-clean                # stop and delete the demo data
```

The demo publishes the same ports as `make up` (3000, 8080): stop one stack before starting the
other.

## 2. A three-minute tour

1. **Overview.** The demo banner says the checks are simulated. *At a glance* and *What was
   caught* count what the gateway did with each answer: passed, flagged, blocked, replaced,
   not checked.
2. **Traffic.** A round of scripted requests arrives about once a minute. Open the row
   *Refund window: 60 days vs 30 days*:
   - the answer, with the contradicted claim marked in place ("60 days");
   - why it was **Flagged**: `contradiction` and `groundedness` critical, and "60" is a number
     the documents do not contain; in block mode it would have been blocked;
   - what it was **checked against**: the refund policy in the system prompt;
   - the **deep audit** a few seconds later, claim by claim.
3. **A blocked answer.** *Invented link blocked in a buffered stream*: the help-center route
   blocks an answer with a link that is not in its documents; the client got "I can't verify
   that answer against our help articles, so I've held it back." instead.
4. **A replaced answer.** *Payments: contradicted answer replaced by a grounded fallback*: the
   first answer contradicted the payments policy, so the gateway asked again with only the
   policy as context, checked the new answer, and sent that one ("5 to 10 business days").
   The trace shows both decisions.
5. **Policies.** Change `support-bot` from Flag to Observe as a draft, review the diff and the
   replay of recent traffic, save it as a new version, then roll back.
6. **Playground.** Pick a route, paste the documents and the answer to check (or try an
   example), optionally act as a stricter mode, and see the decision and the deep audit for
   exactly that text.

## 3. The scenarios

The traffic generator (`cmd/trafficgen`) replays the `demo` set of
[`contracts/demo/scenarios.v1.json`](../contracts/demo/scenarios.v1.json): 26 scenarios, each
with the outcome it expects.

| Outcome | Scenarios |
|---|---|
| **Passed** | a grounded support answer; answers from tool results, a `Context:` block, Anthropic Messages with top-level system documents, legacy completions with `trustagent.context`; two generated answers |
| **Flagged** | the 60-vs-30-day refund window; an invented delivery date; an unsupported detail from a developer-role document; a misquoted warranty; another customer's email; a leaked internal token; a needless refusal; a successful prompt injection; an unapproved credit and date; an off-topic billing answer; a streamed answer flagged by comment and trailers; a block route that is not calibrated, so it flags |
| **Blocked** | an invented link in a buffered stream |
| **Replaced** | the payments answer replaced by a grounded fallback |
| **Would block** | an observe route that records what it would have blocked |
| **Not checked** | no documents at all; documents TrustAgent cannot find (prose in the system prompt, an unknown tag, a clause pasted into the question) |

[contracts/README.md](../contracts/README.md#demo-scenarios) documents the scenario format.

## 4. Check it from the terminal

```bash
make doctor ARGS="--expect demo"
```

Every check should pass: the containers, the proxy, engine and dashboard, demo mode with no
vendor keys, the fast check (`backend mock, state mock`), at least two calibrated targets (the
simulated calibration), the policy, both metrics endpoints (`trustagent_gateway_*` on the proxy,
`trustagent_engine_*` on the engine), recent traces and a completed deep audit. A `WARN` "no
admin yet" only means the setup link is not claimed yet.

Run every scenario once and compare with what it expects (the M-Demo gate does this):

```bash
docker compose -p truthtable-demo exec -T proxy /trafficgen --once --expect
```

It prints `ok` per scenario and exits 1 when one does not match.

To see the headers yourself, replay one scenario by hand: its request, its scripted answer
(`test_response`, allowed because the demo runs in test mode) and its id, which the mocks
answer from:

```bash
curl -sS -D - -o /dev/null http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-TrustAgent-Key: $(python3 scripts/envfile.py get TRAFFICGEN_API_KEY)" \
  -H "X-TrustAgent-Route: support-bot" \
  -H "X-TrustAgent-Scenario: refund-contradiction-flag" \
  -d '{"model": "gpt-4o-mini",
       "messages": [
         {"role": "system", "content": "You are the Acme support assistant. Answer only from the documents below. If they do not cover the question, say so.\n\n<document id=\"refund-policy.md\">Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.</document>"},
         {"role": "user", "content": "Can I still get a refund on my annual plan? I bought it 45 days ago."}],
       "test_response": "You can get a full refund within 60 days of purchase."}' \
  | grep -i '^x-trustagent'
```

Expect `X-TrustAgent-Action: flag`, `X-TrustAgent-Would-Action: block` and, in
`X-TrustAgent-Reasons`, `contradiction:critical`, `groundedness:critical` and
`det:numbers_not_in_evidence`.

## 5. Share it

- **The static replay site** needs no backend at all: the landing page and the dashboard
  replaying a recorded demo session, labelled simulated. Build and preview it with
  `make test-e2e-static`, or `make build-static && cd frontend-react && npx vite preview --mode
  static` and open <http://localhost:4173/TrustAgent/>. This is the safest thing to send
  someone.
- **The live demo on a public URL.** [Cloudflare quick
  tunnels](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) need
  no account. The dashboard's nginx serves `/api`, `/v1`, `/ws` and `/health` on the same
  origin, so one tunnel to port 3000 is enough:

  ```bash
  cloudflared tunnel --url http://localhost:3000   # prints https://<random>.trycloudflare.com
  ```

  Before you share it: claim the setup link yourself first, and restart the demo with the
  tunnel's origin allowed and dashboard writes turned off:

  ```bash
  TRUTHTABLE_ALLOWED_ORIGINS=https://<random>.trycloudflare.com \
  TRUTHTABLE_PUBLIC_URL=https://<random>.trycloudflare.com \
  TRUTHTABLE_READONLY=true make demo OPEN=0
  ```

  Never tunnel Grafana, Prometheus or the metrics ports. Stop `cloudflared` when you are done;
  nothing stays published.

## 6. Known environment caveats

- **TLS-intercepting networks** (some corporate VPNs and antivirus products) break downloads
  inside image builds (apk, pip, npm) with `certificate signed by unknown authority`. Build on
  another network once; the build cache then works offline.
- **Memory.** The demo stack itself stays well under 1 GB, plus 1 to 2 GB for Docker Desktop
  **[estimate]**; the first build peaks higher. See
  [GETTING-STARTED.md](GETTING-STARTED.md#6-how-much-memory-it-needs).
- **Ports in use.** If 3000 or 8080 is taken, set `DASHBOARD_HOST_PORT` / `PROXY_HOST_PORT` in
  `.env`.
