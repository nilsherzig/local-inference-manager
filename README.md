# local-inference-manager (lim)

An on-demand manager and OpenAI-compatible proxy for `llama-server`. A lighter
alternative to llama-swap: it starts a `llama-server` instance when a request
needs it, swaps between models, stops idle instances, and shows what is running
in a dashboard. It adds **no abstraction over llama-cpp** — you give it the exact
flags you would pass to `llama-server`.

## Features

- On-demand start/stop of one `llama-server` instance at a time (pure swap).
- OpenAI-compatible `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`,
  routed to the right model instance (by name or alias).
- Cold-start hold: requests wait while an instance starts, then pass straight to
  `llama-server` (which handles its own slots). Queue depth shown in the UI.
- Bearer-token auth for `/v1/*`; create and revoke tokens in the dashboard.
- Live request log with timings (wall time, prompt/decode speed, tokens) via
  htmx SSE.
- Prometheus `/metrics`: the manager's own metrics plus the active instance's
  `llama-server` metrics, relabelled with `model="..."`.
- Reverse-proxied `llama-server` web UI at `/llama-server/<model>/`.
- Idle instances stop after a configurable TTL.

## Configure

Copy `config.example.yaml` to `config.yaml` and edit. The `cmd` for each model is
the literal `llama-server` command line — no macros, just copy a block to add a
model. `${PORT}` is the only substitution; the manager assigns a free port per
instance.

## Run

```sh
nix-shell            # provides go, tailwindcss, sqlite
make run             # builds the CSS and runs against ./config.yaml
```

Then open http://127.0.0.1:8080.

Flags: `-config <path>` (default `config.yaml`) and `-show-llama-logs` to mirror
each instance's stdout/stderr to this process, prefixed with `[model]`.

```sh
# create a token in the UI (/tokens), then:
curl -H "Authorization: Bearer <token>" http://127.0.0.1:8080/v1/chat/completions \
  -d '{"model":"gemma","messages":[{"role":"user","content":"hi"}]}'
```

## Endpoints

| Path | Auth | Purpose |
|------|------|---------|
| `/` | open | dashboard + live log |
| `/instances`, `/tokens` | open | manage instances / tokens |
| `/playground` | open | send a quick test query to a model, see answer + timings |
| `/models` | open | model list + live status (JSON) |
| `/metrics` | open | Prometheus (own + bundled instance metrics) |
| `/llama-server/<model>/` | open | proxied `llama-server` web UI |
| `/v1/*` | bearer | OpenAI-compatible inference |

## Develop

```sh
make css     # rebuild the vendored Tailwind stylesheet after editing templates
make test    # run all unit tests
```

htmx, the SSE extension and the Tailwind build are vendored under
`internal/web/static/` and embedded in the binary.
