# GitHub repository metadata

What the repository page shows before anyone opens the README. Apply it at launch, after the
[launch checklist](launch-checklist.md)'s release section, so the website link and the social
card point at a real, recorded demo.

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

With the GitHub CLI, signed in as the owner:

```bash
gh repo edit timkush1/TrustAgent \
  --description "Open-source hallucination gateway for LLM apps: a Go proxy checks every answer with a fast decision model (Jev) and escalates only risky ones to a claim-level LLM audit." \
  --homepage "https://timkush1.github.io/TrustAgent/" \
  --enable-wiki=false --enable-projects=false \
  --add-topic llm,hallucination-detection,ai-gateway,guardrails,llm-evaluation,llm-as-a-judge \
  --add-topic rag,golang,react,grpc,langgraph,openrouter,jev,ai-safety,prompt-injection,observability
gh repo view timkush1/TrustAgent --json description,homepageUrl,repositoryTopics
```

The social preview has no CLI: Settings → General → Social preview → Edit → upload
`docs/launch/og.png`.
