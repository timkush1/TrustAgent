# Getting started

This guide takes you from a fresh Windows laptop to TrustAgent running on it. You start with the
**demo**, which needs no keys and costs nothing, and then switch to **real mode** with your own
OpenRouter key. Everything runs in Docker on your own machine; there is nothing to deploy.

**Contents:** [1. What you need](#1-what-you-need) ·
[2. Start the demo](#2-start-the-demo-no-keys-0) ·
[3. What you will see](#3-what-you-will-see) ·
[4. Stop the demo](#4-stop-the-demo) ·
[5. Real mode](#5-real-mode-your-own-openrouter-key) ·
[6. Memory](#6-how-much-memory-it-needs) ·
[7. Troubleshooting](#7-troubleshooting)

## 1. What you need

| What | Why | Check it with |
|---|---|---|
| Windows 10 or 11 with **WSL 2** and a Linux distribution (Ubuntu is fine) | TrustAgent's commands run in a Linux terminal | `wsl -l -v` in PowerShell shows your distribution with VERSION 2 |
| **Docker Desktop**, with WSL integration turned on | Builds and runs every part of TrustAgent | `docker compose version` in the WSL terminal prints 2.24 or newer |
| `git`, `make` and `python3` (3.9 or newer) in WSL | The `make` commands and the small setup scripts | `git --version && make --version && python3 --version` |
| About 8 GB of RAM and 10 GB of free disk | See [section 6](#6-how-much-memory-it-needs) | |

You do **not** need Go, Node.js or a Python virtual environment: Docker builds everything. (Those
are only for working on the code; see the component READMEs.)

**Set up Docker Desktop for WSL, once:**

1. Install Docker Desktop from [docker.com](https://www.docker.com/products/docker-desktop/) and
   start it. Wait until it says **Engine running**.
2. Open Docker Desktop → **Settings** → **Resources** → **WSL integration**. Turn on the switch
   next to your Linux distribution, then click **Apply & restart**.
3. Close your WSL terminal and open a new one, then check:

   ```bash
   docker compose version          # 2.24 or newer
   docker run --rm hello-world     # prints "Hello from Docker!"
   ```

4. If `make` or `python3` is missing, install them:

   ```bash
   sudo apt update && sudo apt install -y make python3 git
   ```

**Get the code.** Keep it inside the Linux file system (your home folder), not under `/mnt/c/...`:
Docker builds are much slower on the Windows drive.

```bash
mkdir -p ~/projects && cd ~/projects
git clone https://github.com/timkush1/TrustAgent.git
cd TrustAgent
```

## 2. Start the demo (no keys, $0)

From the repository folder, run:

```bash
make demo
```

This one command:

1. Creates `.env` from [`.env.example`](../.env.example) if you don't have one yet, and fills in
   the passwords the stack needs (they are random and never printed).
2. Builds the images from your checkout. **The first run takes a while** (about 5–15 minutes
   [estimate], depending on your laptop and connection); later runs reuse the build cache and
   are ready in a minute or two.
3. Starts the demo stack: the proxy, the audit engine, a database, the dashboard and a small
   traffic generator. They run as their own Docker project, `truthtable-demo`, so demo data never
   mixes with real data.
4. Waits until every part reports healthy, then opens a **one-time setup link** such as
   `http://localhost:3000/app/setup#token=…`. Click **Create the admin key**: the dashboard shows
   your admin key once (keep it) and signs this browser in, so you can also edit policies and use
   the Playground. When an admin already exists it opens the dashboard at
   **<http://localhost:3000>** instead. If no browser opens, open the printed link yourself;
   `make demo-setup-url` prints a new one.

Everything in the demo is simulated: the fast check and the deep-audit judge are mocks, and no
vendor key reaches the demo containers, even if your `.env` holds one. It costs $0.

## 3. What you will see

- A **Demo stack** banner at the top: *"Mock checks and a mock judge: every number here is
  simulated."*
- **Traffic** (in the left navigation): the traffic generator sends a round of about 20 scripted
  requests about once a minute, so new rows keep appearing. Each row has an outcome pill; a round
  starts with these three:

  | Scenario | Outcome | Why |
  |---|---|---|
  | A support answer that matches the refund policy | **Passed** | Everything in the answer is in the documents sent with the request |
  | *Refund window: 60 days vs 30 days* | **Flagged** | The answer contradicts the document. The app got the answer with a risk warning (`X-TrustAgent-Action: flag`) |
  | *No documents: not checked* | **Not checked** | There was nothing to check against, so TrustAgent says so instead of guessing |

  The rest of the round shows other checks: a leaked personal email or token, a needless refusal,
  a promise the billing rules do not allow, an off-topic answer, documents in tool results or a
  `Context:` block, prompts whose documents TrustAgent cannot find (*Not checked*), and an observe
  route that only records what it *would* have blocked, an answer **Blocked** for an invented
  link, a payments answer **Replaced** by a grounded fallback, streamed answers and the Anthropic
  API. [DEMO.md](DEMO.md) has a five-minute tour, and
  [contracts/README.md](../contracts/README.md#demo-scenarios) lists every scenario.
- **Click a row** to open the trace: the answer, the reasons for its outcome, what it was
  **checked against** (for example the system-prompt document), and the **deep audit**, which
  arrives a few seconds later and explains the verdict claim by claim.
- Pages that are not built yet show a short placeholder that says what they will do and which
  phase brings them. There are no dead buttons.

**Check it from the terminal** (optional):

```bash
python3 scripts/doctor.py --expect demo
```

Every check should say `PASS` (a `SKIP` is fine). A `WARN` such as "still starting" usually
clears when you run it again a minute later, and "no admin yet" clears once you claim the setup
link. One `WARN` stays on purpose: `policy.state` lists a downgrade on the demo's `help-center`
route, which is left uncalibrated to show what that does. A `FAIL` comes with a command that
fixes it.

## 4. Stop the demo

```bash
make demo-down      # stop the demo; its data is kept for next time
make demo-clean     # stop it and delete the demo data
```

Other handy commands: `make demo-logs SERVICE=engine` follows one part's log (leave out
`SERVICE=` for all of them), and `make help` lists every command.

## 5. Real mode: your own OpenRouter key

In real mode TrustAgent checks your own app's answers. One OpenRouter key runs both the fast
check (Jev) and the deep-audit judge.

> **Without a key** `make up` still runs, but the fast check is off: answers pass through
> unchecked and show as **Not checked** (`X-TrustAgent-Status: skipped:decision_model_off`).
> The setup wizard also offers **Try it without a key (simulated checks, $0)**, which runs both
> checks on mocks and labels every number Simulated.

1. **Get an OpenRouter account with credit.** Sign in at [openrouter.ai](https://openrouter.ai)
   and add $5 at [openrouter.ai/settings/credits](https://openrouter.ai/settings/credits). That
   covers about 1,600 answers at the default settings **[estimate]**.
2. **Stop the demo.** Both stacks use the same ports:

   ```bash
   make demo-down
   ```

3. **Keep the master key safe (once).** Keys you save in the dashboard are encrypted with a master
   key that lives in the `trustagent-secrets` Docker volume. Either back that volume up, or put
   the master key in `.env` yourself before the first start:

   ```bash
   echo "TRUTHTABLE_SECRET_KEY=$(python3 -c 'import base64, secrets; print(base64.b64encode(secrets.token_bytes(32)).decode())')" >> .env
   ```

   Without either, losing the volume means entering your keys again
   ([SECURITY.md](SECURITY.md#master-key)).
4. **Start TrustAgent:**

   ```bash
   make up
   ```

   This starts the proxy, the engine, the database and the dashboard (no traffic generator), and
   opens a one-time setup link (`http://localhost:3000/app/setup#token=…`). If you missed it,
   `make setup-url` prints a new one (each replaces the last).
5. **Walk the setup wizard.**
   - **Step 1, admin key.** Click **Create the admin key**. The dashboard shows it once: keep it in
     your password manager. Later you sign in at `/app/setup` with it.
   - **Step 2, connect.** Click **Connect OpenRouter**, sign in on openrouter.ai and approve. You
     come back to TrustAgent with a new key, stored encrypted and already checked. Then open
     [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys) and give that key a
     **credit limit** (for example $2): keys created this way have none. **Paste a key** is on the
     same screen if you prefer an existing key.
   - **Step 3, connect your app.** The wizard shows the base URL and a code snippet, and mints an
     ingest key for your app when none exists yet (shown once). Otherwise create one on
     **Settings → Access**, or use the traffic generator's key from `.env`
     (`python3 scripts/envfile.py get TRAFFICGEN_API_KEY`) for a first test.
6. **Check the connection:** `make doctor` shows which fast-check backend is active and why, its
   snapshot, state, latency and the last 24 hours' calls and cost, whether every saved key
   decrypts, where the judge's key comes from, and today's spend against the budgets.

**Prefer `.env`?** Put `OPENROUTER_API_KEY=sk-or-v1-...` in `.env` before `make up` (find the
line `# OPENROUTER_API_KEY=` and remove the `# `), or run `make configure WITH_SECRETS=1`, which
asks for the key at a hidden prompt. A key in `.env` always wins over the dashboard, which then
shows it as read-only. Never paste a key into a chat, an issue, a commit or a screenshot.

**Optional: let the dashboard read traffic without signing in.** Add
`TRUTHTABLE_PUBLIC_READ=true` to `.env`. This is safe on your laptop because TrustAgent only
listens on 127.0.0.1 (`BIND_ADDR`); remove the line before you ever set `BIND_ADDR=0.0.0.0`.
Without signing in, the dashboard shows outcomes and reasons but hides the prompts and answers.

**Send a first request.** Point your app at the proxy instead of the LLM provider:
base URL `http://localhost:8080/v1`. Two things change for your app: it sends its TrustAgent key
(an ingest key, see step 5) in an `X-TrustAgent-Key` header, and it sends the documents the answer
must stick to, so there is something to check against. A quick test from the terminal, with
OpenRouter as the LLM too:

```bash
# once: send LLM calls through OpenRouter as well (then run `make up` again)
echo 'TRUTHTABLE_UPSTREAM_URL=openrouter' >> .env

read -rsp 'OpenRouter key (for the LLM call): ' LLM_KEY; echo          # hidden, not in history
read -rsp 'TrustAgent ingest key: ' TA_KEY; echo
curl -sS -D - http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $LLM_KEY" \
  -H "X-TrustAgent-Key: $TA_KEY" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "How long do I have to ask for a refund on an annual plan?"}],
    "trustagent": {"context": [{"id": "refund-policy.md", "text": "Annual plans: refund within 30 days of purchase."}]}
  }'
```

The response is the normal LLM answer, plus `X-TrustAgent-*` headers that say what TrustAgent
did; the same request then appears in Traffic, and the wizard's step 3 turns from waiting to
received. More keys for more apps: **Settings → Access** (or a `label=key` entry in
`TRUTHTABLE_API_KEYS` in `.env`). The LLM call itself is billed by OpenRouter as usual; TrustAgent's own costs are in
[JEV-ACCESS.md](JEV-ACCESS.md#costs-estimate).

**Stop:** `make down` (keeps your data). `make clean-data` deletes all of it.

## 6. How much memory it needs

The default stack is small enough for an 8 GB laptop. These are estimates; see real numbers any
time with `docker stats --no-stream`.

| Part | Memory while running [estimate] | Hard cap in `docker-compose.yml` |
|---|---|---|
| Proxy | 30–60 MB | 512 MB |
| Audit engine | 200–400 MB | 4 GB |
| Database (Postgres) | 50–100 MB | none |
| Dashboard | 10–20 MB | 256 MB |
| Traffic generator (demo only) | about 10 MB | 64 MB |
| **Total** | **well under 1 GB** | |

- **Docker Desktop itself** needs about 1–2 GB on top of that.
- **The first build is the peak**: about 2–3 GB [estimate] for a few minutes while the three
  images compile. Close heavy apps during the first `make demo`.
- **Add-ons cost more.** They are off by default and switched on with `COMPOSE_PROFILES` in
  `.env`:

  | Add-on | Extra memory [estimate] | On an 8 GB laptop |
  |---|---|---|
  | `redis` | about 20 MB | Fine |
  | `observability` (Prometheus and Grafana) | 300–500 MB | Fine |
  | `knowledge` (document store and embedding model) | 1.5–2.5 GB | One add-on at a time |
  | `local-llm` (Ollama judge) | 2–3 GB, plus a 1.3 GB download | Not recommended; use the OpenRouter judge |

**How much can Docker use?** In WSL, `free -h` shows the memory Linux and Docker share. By default
WSL 2 takes up to half of the laptop's RAM: about 4 GB on an 8 GB laptop, which is enough for
the demo and the default stack. If builds get killed (exit code 137) or you turn on the
`knowledge` add-on, raise the limit a little, keeping at least 3 GB for Windows. On an 8 GB
laptop, create `C:\Users\<you>\.wslconfig` with:

```ini
[wsl2]
memory=5GB
```

then run `wsl --shutdown` in PowerShell and start Docker Desktop again. Close heavy Windows
apps (extra browser windows, for example) while TrustAgent builds.

## 7. Troubleshooting

**`docker: command not found` in the WSL terminal.** Docker Desktop is not running, or WSL
integration is off for this distribution.

1. Start Docker Desktop and wait for **Engine running**.
2. Docker Desktop → Settings → Resources → **WSL integration** → turn on your distribution →
   **Apply & restart**.
3. Close the WSL terminal, open a new one and run `docker compose version`.

If it still fails, run `wsl --shutdown` in PowerShell, start Docker Desktop again and reopen the
terminal. Don't install a second Docker inside WSL (`apt install docker.io`): it conflicts with
Docker Desktop.

**`Cannot connect to the Docker daemon`.** Docker Desktop is installed but not started, or still
starting. Start it and wait for **Engine running**.

**`docker compose version` is older than 2.24, or you see an error about `!reset`.** Update Docker
Desktop; the demo's compose file needs Compose 2.24 or newer.

**A port is already in use** (`port is already allocated`, `address already in use` or `ports are
not available`). The stacks publish these ports on your machine:

| Port | Used by | Move it with (in `.env`) |
|---|---|---|
| 3000 | Dashboard | `DASHBOARD_HOST_PORT=3100` |
| 8080 | Proxy | `PROXY_HOST_PORT=8180` |
| 5432 | Database (`make up` only) | `POSTGRES_HOST_PORT=5433` |

1. Is it the other TrustAgent stack? Only one of them can run at a time: `make down` or
   `make demo-down`, then start the one you want.
2. Otherwise, find what holds the port: `ss -ltnp | grep ':3000'` in WSL, or
   `netstat -ano | findstr :3000` in PowerShell (the last column is the process id).
3. Or simply move TrustAgent: add the line from the table to `.env` and run `make demo` (or
   `make up`) again. The dashboard address follows the new port, for example
   <http://localhost:3100>.

If nothing uses the port, Windows may have reserved it. `netsh interface ipv4 show
excludedportrange protocol=tcp` in PowerShell lists the reserved ranges; pick a port outside them.

**`make demo` stops with "unhealthy" or times out.** See which part failed and why:

```bash
python3 scripts/doctor.py --expect demo
make demo-logs SERVICE=engine      # or proxy, dashboard, postgres
```

On a slow first start, give it more time: `make demo WAIT_TIMEOUT=900`.

**The build stops with `killed` or exit code 137.** Docker ran out of memory. Close other apps,
give WSL more memory (see [section 6](#6-how-much-memory-it-needs)) and run `make demo` again;
it continues from where it stopped.

**A warning about `POSTGRES_PASSWORD` and an existing volume.** The database keeps the password
it was first created with, and your `.env` now has a different one (for example because `.env`
was deleted). Put the old password back in `.env`, or delete the old data: `make demo-clean` for
the demo, `make clean-data` for the real stack (**this deletes all TrustAgent data**).

**No browser opens.** Open the printed setup link, or <http://localhost:3000> (or your
`DASHBOARD_HOST_PORT`), yourself. Use `make demo OPEN=0` to only print the address.

**Traffic stays empty in the demo.** The first round arrives within a few seconds of the stack
becoming healthy. If nothing shows after a minute, check the generator:
`make demo-logs SERVICE=trafficgen`.

**Everything is slow.** Check that the repository is under your Linux home folder (`pwd` prints
`/home/...`, not `/mnt/c/...`), and see [section 6](#6-how-much-memory-it-needs).

## Next steps

- [JEV-ACCESS.md](JEV-ACCESS.md): the fast check, its costs, backends, errors and fixes.
- [GATEWAY.md](GATEWAY.md): what your app gets back: modes, headers, streaming, block and fallback.
- [CONFIGURATION.md](CONFIGURATION.md): every setting, its default and what it does.
- [ARCHITECTURE.md](ARCHITECTURE.md): how the parts fit together.
