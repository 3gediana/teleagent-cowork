# A3C Embedding Microservice

Tiny FastAPI sidecar that hosts **`BAAI/bge-base-zh-v1.5`** (768-dim Chinese
+ English sentence embeddings) for the Go backend.

## Why a sidecar

The Go server cannot easily host a PyTorch model. Running a small local
HTTP service is the simplest way to get sentence embeddings into the
refinery / injection pipeline. The Go side only needs an HTTP client.

## Deploy (3 commands)

```powershell
cd platform\embedder
python -m venv .venv ; .\.venv\Scripts\Activate.ps1 ; pip install -r requirements.txt
python app.py
```

On first launch, the model (~400MB) is downloaded from HuggingFace into
`./models/` (gitignored). Subsequent launches are **fully offline**.

### Behind the Great Firewall

Set a mirror before launching:

```powershell
$env:HF_ENDPOINT = "https://hf-mirror.com"
python app.py
```

### Re-use an already-downloaded copy

If you've already got the model on disk (e.g. another HF cache), point
`BGE_MODEL` at the snapshot directory to skip the download entirely:

```powershell
$env:BGE_MODEL = "C:\path\to\snapshots\<hash>"
python app.py
```

### Offline / air-gapped install

1. On a machine with network: `huggingface-cli download BAAI/bge-base-zh-v1.5 --local-dir ./models/bge`
2. Copy `platform/embedder/models/` to the target machine
3. Run `python app.py` — no network needed

## Config (env vars)

| Var | Default | Purpose |
|-----|---------|---------|
| `BGE_MODEL` | `BAAI/bge-base-zh-v1.5` | HF model ID or absolute path to snapshot dir |
| `BGE_CACHE_DIR` | `./models/` | Where to download / look up model files |
| `BGE_DEVICE` | `auto` | `cuda` / `cpu` / `auto` (auto picks GPU if present) |
| `BGE_BATCH` | `32` | Encode batch size |
| `EMBEDDER_PORT` | `3011` | HTTP listen port |
| `HF_ENDPOINT` | — | Custom HuggingFace mirror |

## API

### `GET /health`

```json
{
  "status": "ok",
  "model": "BAAI/bge-base-zh-v1.5",
  "cache_dir": "D:\\...\\platform\\embedder\\models",
  "dim": 768,
  "device": "cuda",
  "batch_size": 32
}
```

### `POST /embed`

```json
{ "texts": ["修复 auth 的 401 bug"], "is_query": false }
```

- `is_query=true` — search query (applies bge-zh's query prefix)
- `is_query=false` — document being indexed

Response vectors are **L2-normalized**, so cosine similarity reduces to a
plain dot product on the Go side.

Server caps `len(texts) ≤ 256` per request — chunk larger jobs client-side.

## Integration

The Go backend expects this service at `http://127.0.0.1:3011` by default.
Override via `A3C_EMBEDDER_URL` on the Go side.
