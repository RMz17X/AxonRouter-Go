# AxonRouter-Go: API Reference & Dialect Protocol

**Version:** `1.9.59`  
**Router Implementation:** [`internal/handlers/router.go`](file:///workspaces/AxonRouter-Go/internal/handlers/router.go), [`internal/handlers/chat/handler.go`](file:///workspaces/AxonRouter-Go/internal/handlers/chat/handler.go)

---

## 1. Unified Dialect Routing

AxonRouter-Go accepts incoming requests across 4 major LLM dialects and normalizes them into internal unified request representations:

```mermaid
flowchart LR
  Client[Client Application] --> Gateway{AxonRouter-Go}
  Gateway -->|OpenAI Dialect| R1["POST /v1/chat/completions"]
  Gateway -->|Claude Dialect| R2["POST /messages"]
  Gateway -->|Ollama Dialect| R3["POST /api/chat"]
  Gateway -->|Responses Dialect| R4["POST /responses"]

  R1 --> Pipeline[Unified Translation & Routing Engine]
  R2 --> Pipeline
  R3 --> Pipeline
  R4 --> Pipeline

  Pipeline --> Upstream[Upstream Provider Adapter]
```

---

## 2. Proxy Surface Endpoints

### 2.1 Chat & Completion Endpoints

| Method | Path | Dialect | Description | Auth Required |
|---|---|---|---|---|
| `POST` | `/v1/chat/completions` | OpenAI | Full OpenAI-compatible chat completion | Client API Key |
| `POST` | `/chat/completions` | OpenAI | Virtual prefix alias for `/v1/chat/completions` | Client API Key |
| `POST` | `/messages` | Claude | Anthropic Messages API compatible | Client API Key |
| `POST` | `/v1/messages` | Claude | Prefixed Anthropic Messages API | Client API Key |
| `POST` | `/messages/count_tokens` | Claude | Token estimation for prompt context | Client API Key |
| `POST` | `/api/chat` | Ollama | Ollama native chat payload format | Client API Key |
| `POST` | `/responses` | Custom | State-preserving conversation responses | Client API Key |
| `POST` | `/responses/compact` | Custom | Memory compactor for active conversations | Client API Key |

### 2.2 Model Catalog Endpoints

| Method | Path | Description | Response Shape |
|---|---|---|---|
| `GET` | `/v1/models` | List all active configured models | `{"object":"list","data":[{"id":"...","owned_by":"..."}]}` |
| `GET` | `/v1/models/{id}` | Inspect specific model metadata & limits | Single model descriptor |
| `GET` | `/v1/models/{kind}` | Filter models by kind: `llm`, `embedding`, `image`, `tts`, `stt`, `video` | Filtered list |
| `GET` | `/api/models/catalog-sync` | Status of remote catalog sync | Sync timestamp and state |
| `POST` | `/api/models/catalog-sync` | Force refresh model definitions from upstream | Trigger confirmation |

### 2.3 Batch & File APIs (OpenAI Parity)

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/files`, `/files` | Upload file for batch execution |
| `GET` | `/v1/files`, `/files` | List uploaded files |
| `GET` | `/v1/files/{file_id}`, `/files/{file_id}` | Retrieve file metadata |
| `GET` | `/v1/files/{file_id}/content` | Download raw file content |
| `DELETE` | `/v1/files/{file_id}` | Purge uploaded file |
| `POST` | `/v1/batches`, `/batches` | Submit offline asynchronous batch |
| `GET` | `/v1/batches`, `/batches` | List batches and current execution status |
| `GET` | `/v1/batches/{batch_id}` | Retrieve batch progress metrics |
| `POST` | `/v1/batches/{batch_id}/cancel` | Abort in-flight batch job |

---

## 3. Streaming (Server-Sent Events)

When `stream: true` is provided in the request payload:
- **Response Headers:**
  - `Content-Type: text/event-stream`
  - `Cache-Control: no-cache, no-transform`
  - `Connection: keep-alive`
  - `X-Accel-Buffering: no` (disables buffering in Nginx reverse proxies)
- **Event Framing:**
  - Data chunks formatted as `data: {"id":"...","choices":[{"delta":{"content":"..."}}]}\n\n`
  - Terminal event formatted as `data: [DONE]\n\n`
- **Client Disconnect Handling:**
  - The handler monitors `r.Context().Done()`.
  - Upstream HTTP transport connection is immediately cancelled upon client disconnect to release provider socket and prevent wasted upstream token spend.

---

## 4. Standard Error Envelopes

All errors return JSON envelopes conforming to standard API client expectations:

```json
{
  "error": {
    "message": "Invalid API key provided",
    "type": "authentication_error",
    "code": "invalid_api_key",
    "param": null
  }
}
```

### HTTP Status Codes:
- `400 Bad Request`: Malformed JSON or unparseable dialect payload.
- `401 Unauthorized`: Missing or invalid client API key / expired session.
- `403 Forbidden`: Insufficient permissions or remote default password block.
- `404 Not Found`: Model or requested route not configured in active registry.
- `429 Too Many Requests`: RPM/TPM limit exceeded or provider rate limit encountered.
- `502 Bad Gateway`: Upstream provider unreachable, timeout, or TLS handshake error.
- `504 Gateway Timeout`: Upstream provider exceeded global deadline.

---

## 5. Rate Limiting Headers

When `RequireRateLimit` middleware is active, outgoing responses include:
- `X-RateLimit-Limit-Requests`: Configured requests per minute (RPM).
- `X-RateLimit-Remaining-Requests`: Remaining request quota in sliding window.
- `X-RateLimit-Limit-Tokens`: Configured tokens per minute (TPM).
- `X-RateLimit-Remaining-Tokens`: Remaining token quota in sliding window.
- `Retry-After`: Seconds until quota bucket replenishment (on `429`).
