# local-inference-manager (lim)

> [!WARNING]
> **WORK IN PROGRESS**
> Alternative to [llama-swap](https://github.com/mostlygeek/llama-swap)

- Are you tired of keeping 20 different llama-server configs and scripts?
- Would you like to share access to your local models with friends and colleges, but in a controlled way?
- Do you happen to be a member of the permanent underclass (less than 500GB VRAM)?
- Would you like to keep different models ready to load on demand and have them unload after some idle time? 


https://github.com/user-attachments/assets/3cac1f51-e293-4643-ab8c-c203b690d7e4

In this demo you can see me create a new auth token and send an example request to the proxy. This starts a new llama-server instance, answers my request and creates some logs and metrics while doing so.

## Features:

- prometheus exporter with llama-server instance metrics aggregation (and some new global ones) 
- full llama-server stop on instance idle (actually clears vram and allows your GPU to sleep)
- on demand instance start, no need to send an extra start request
- auth tokens with per token logs & metrics
- config has zero abstraction, all llama-server args are visible. you can use your existing configs
- models are pre-downloaded with the HuggingFace CLI (fast `hf_transfer` backend, progress bar) and loaded with explicit `-m` paths
- supports multiple alias names for your models
- run with `--show-llama-logs` to get the full llama-server logs to stdout, nothing is hidden
- the proxy webinterface works on mobile


## Config format: 

Please check [example config](./config.example.yaml) for more details.

Here is a config for qwen3.6 27b, as you can see this project is truely just a manager and doesnt try to replace anything. Feel free to use the most cursed llama-server args someone told you in a dream:

```yaml
manager:
  # where downloads land: <models_dir>/<repo>/<file>. In the container this is a
  # mounted volume so downloads survive restarts.
  models_dir: "/data/models"
models:
  qwen3.6-27b:
    # lim pre-downloads these repos with the HuggingFace CLI before serving
    # (repo:quant, same shorthand as -hf). Load them with explicit -m paths.
    downloads:
      - unsloth/Qwen3.6-27B-MTP-GGUF:Q4_K_M
    cmd: |
      /app/llama-server
      --host 127.0.0.1
      --port ${PORT}
      -ngl 99
      --jinja
      --metrics
      -fa on
      --cache-type-k q8_0
      --cache-type-v q8_0
      --cache-reuse 256
      --no-mmap
      --spec-type draft-mtp
      -m /data/models/unsloth/Qwen3.6-27B-MTP-GGUF/Qwen3.6-27B-MTP-GGUF-Q4_K_M.gguf
      --spec-draft-n-max 2
      --ctx-size 131072
      --temp 0.6
      --top-p 0.95
      --top-k 20
      --min-p 0
      --reasoning-preserve
      --repeat-penalty 1
    ttl: 300
    aliases:
      - qwen
      - qwen3.6
      - qwen3.6-27b-mtp
```

Downloads use the fast `hf_transfer` backend (chunked, parallel — see the
[reddit writeup](https://www.reddit.com/r/LocalLLaMA/comments/1ise5ly/)) and show
a progress bar during startup. A model may list several `downloads` entries when
its llama-server config needs more than one file — a main model plus a
speculative-decoding drafter (`-md`), or a multimodal projector (`--mmproj`). See
the [example config](./config.example.yaml) for a drafter setup. The exact `.gguf`
filename for `-m` is whatever the repo ships; after the first download run
`ls <models_dir>/<repo>/` to see it.

## Install / Deploy

### Prebuilt images

GitHub Actions builds and pushes an image on every push to `main`. Two GPU
variants are published to the GitHub Container Registry:

```
ghcr.io/nilsherzig/local-inference-manager:cuda      # NVIDIA (CUDA)
ghcr.io/nilsherzig/local-inference-manager:vulkan    # anything Vulkan (AMD, Intel, NVIDIA)
```

Each variant is also tagged per commit as `sha-<commit>-cuda` / `sha-<commit>-vulkan`
if you want to pin a specific build. Get a list of every published tag on the
[Packages page](https://github.com/nilsherzig/local-inference-manager/pkgs/container/local-inference-manager)
of the repo.

The image builds `lim` on top of the upstream `llama.cpp` server image, so
`/app/llama-server` is already inside. `lim` runs it on demand and proxies
requests to it.

### Example `docker run`

Write a `config.yaml` (see [example config](./config.example.yaml)), then set
`manager.listen` to `0.0.0.0:8080` so the proxy is reachable from outside the
container.

CUDA:

```sh
docker run -d \
  --name lim \
  --gpus all \
  -p 8080:8080 \
  -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -v "$PWD/data:/data" \
  ghcr.io/nilsherzig/local-inference-manager:cuda
```

Vulkan (AMD/Intel/NVIDIA via `/dev/dri`):

```sh
docker run -d \
  --name lim \
  --device /dev/dri \
  -p 8080:8080 \
  -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -v "$PWD/data:/data" \
  ghcr.io/nilsherzig/local-inference-manager:vulkan
```

> [!TIP]
> This is a minimal starting point. Paste it into your favorite LLM and ask it
> to turn it into whatever you actually run.
