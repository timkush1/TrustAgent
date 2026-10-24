# GitHub repository metadata

What the repository page shows before anyone opens the README. Apply it at sitting S3c (step G12
of the [launch checklist](launch-checklist.md)), after the release, so the website link and the
social card point at a real, recorded demo.

## Values

| Field | Value |
|---|---|
| Description (169 characters; GitHub allows 350) | Open-source hallucination gateway for LLM apps: a Go proxy checks every answer with a fast decision model (Jev) and escalates only risky ones to a claim-level LLM audit. |
| Website | `https://timkush1.github.io/TrustAgent/` (the Pages site; leave empty until Pages is on) |
| Topics (16; GitHub allows 20) | `llm`, `hallucination-detection`, `ai-gateway`, `guardrails`, `llm-evaluation`, `llm-as-a-judge`, `rag`, `golang`, `react`, `grpc`, `langgraph`, `openrouter`, `jev`, `ai-safety`, `prompt-injection`, `observability` |
| Social preview | `docs/launch/og.png`, captured from the recorded dataset ([og-spec.md](og-spec.md)) |
| Releases, Packages | Shown in the sidebar (the GHCR images link back through their `org.opencontainers.image.source` label) |
| Deployments | Shown once Pages has deployed (environment `github-pages`) |
| Wiki, Projects | Off: the docs live in `docs/`, and planning in `docs/PLAN.md` |

Why these words:
- **"hallucination gateway"** is the one-line category: it sits in the request path (a gateway),
  not beside it (an observability tool).
- **"fast decision model (Jev)"** names the model once, with what it is, so the topic `jev` makes
  sense to someone who has never heard of it.
- **"escalates only risky ones"** is the cost story without a number: a number in the
  description could not carry its label.

## Apply it

In the web UI (the GitHub CLI is optional and not installed here):

1. On the repository page, the **gear next to About**:
   - **Description**: the description above;
   - **Website**: `https://timkush1.github.io/TrustAgent/`;
   - **Topics**: type the 16 topics above one by one (Enter after each);
   - under "Include in the home page", keep **Releases**, **Packages** and **Deployments**
     ticked;
   - Save changes.
2. **Settings → General → Social preview → Edit** → upload `docs/launch/og.png`.
3. **Settings → General → Features**: untick **Wikis** and **Projects**.

With the GitHub CLI, if it is installed and signed in as the owner, the same in one go:

```bash
gh repo edit timkush1/TrustAgent \
  --description "Open-source hallucination gateway for LLM apps: a Go proxy checks every answer with a fast decision model (Jev) and escalates only risky ones to a claim-level LLM audit." \
  --homepage "https://timkush1.github.io/TrustAgent/" \
  --enable-wiki=false --enable-projects=false \
  --add-topic llm,hallucination-detection,ai-gateway,guardrails,llm-evaluation,llm-as-a-judge \
  --add-topic rag,golang,react,grpc,langgraph,openrouter,jev,ai-safety,prompt-injection,observability
```

Check it without signing in (the public API):

```bash
curl -s https://api.github.com/repos/timkush1/TrustAgent | python3 -c \
  'import json,sys; r=json.load(sys.stdin); print(r["description"]); print(r["homepage"]); print(len(r["topics"]), sorted(r["topics"]))'
```

It prints the description, the website and `16` with the topics. The social preview has no API:
open the repository link in LinkedIn's Post Inspector to see the card.
