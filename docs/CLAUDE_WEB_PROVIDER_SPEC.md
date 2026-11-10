# CLAUDE-WEB PROVIDER SPECIFICATION & IMPLEMENTATION BLUEPRINT (AxonRouter-Go)

Dokumen ini mendefinisikan arsitektur dan panduan implementasi untuk menambahkan provider **`claude-web`** ke dalam **AxonRouter-Go**, di mana **Chroma** bertindak sebagai *Identity & Session Vault* (penyedia cookie dan browser fingerprint), dan **AxonRouter-Go** bertindak sebagai *LLM Router & Translation Gateway*.

---

## 1. Arsitektur & Pembagian Tugas

```
┌────────────────────────────────────────────────────────┐
│                        CHROMA                          │
│  - Browser Profiler (Fingerprint, Proxy, Canvas, WebGL)│
│  - User Login ke claude.ai di browser asli             │
│  - Ekstraksi Cookie: sessionKey (sk-ant-sid01-...)     │
│  - Push Sync ke AxonRouter via REST API                │
└──────────────────────────┬─────────────────────────────┘
                           │ POST /api/providers/claude-web/connections
                           ▼
┌────────────────────────────────────────────────────────┐
│                   AXONROUTER-GO                        │
│  - Provider: claude-web (CategoryWebCookie)            │
│  - Connection Pool (Multi-Akun Claude, failover, quota)│
│  - Protocol Translator (OpenAI / Anthropic -> Web SSE) │
│  - Upstream Engine: https://api.claude.ai              │
│  - Output: /v1/chat/completions & /v1/messages         │
└────────────────────────────────────────────────────────┘
```

---

## 2. Parameter Kredensial & Autentikasi

### 2.1 Format Data Koneksi
Setiap koneksi `claude-web` di AxonRouter disimpan dalam tabel SQLite `provider_connections`:
- **`provider`**: `"claude-web"`
- **`auth_type`**: `"cookie"`
- **`data`** (JSON payload):
  ```json
  {
    "cookie": "sessionKey=sk-ant-sid01-xxxxxxxxxx; cf_clearance=yyyyy",
    "sessionKey": "sk-ant-sid01-xxxxxxxxxx",
    "organizationUuid": "00000000-0000-0000-0000-000000000000",
    "userAgent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
    "proxyUrl": "http://user:pass@host:port"
  }
  ```

### 2.2 Aturan Cookie
1. **`sessionKey` (Wajib):** Dimulai dengan prefiks `sk-ant-sid01-`. Cookie ini merupakan JWT session token akun Claude.ai.
2. **`cf_clearance` (Opsional):** Jika IP / Proxy server memerlukan token Cloudflare bypass.
3. **`organizationUuid` (Auto-discovered):** Jika belum ada di payload, AxonRouter mengambilnya secara otomatis via endpoint discovery.

---

## 3. Upstream Endpoints `claude.ai`

Base URL: `https://api.claude.ai`

| Aksi | Method | Endpoint | Keterangan |
| :--- | :--- | :--- | :--- |
| **Org Discovery** | `GET` | `/api/organizations` | Mengambil daftar organisasi akun (`uuid`). |
| **Account Info** | `GET` | `/api/auth/current_account` | Validasi apakah session masih valid/login. |
| **Create Chat** | `POST` | `/api/organizations/{org_id}/chat_conversations` | Menginisialisasi session percakapan baru. Body: `{"uuid": "<uuidv4>", "name": ""}`. |
| **Stream Completion** | `POST` | `/api/organizations/{org_id}/chat_conversations/{chat_id}/completion` | Mengirim prompt dan menerima stream token model (SSE). |
| **Delete Chat** | `DELETE` | `/api/organizations/{org_id}/chat_conversations/{chat_id}` | Membersihkan percakapan setelah selesai (opsional). |

---

## 4. Header Wajib Upstream

Upstream `api.claude.ai` membutuhkan header browser yang konsisten:
```http
Accept: text/event-stream, text/plain, */*
Accept-Language: en-US,en;q=0.9
Content-Type: application/json
Cookie: sessionKey=sk-ant-sid01-...; cf_clearance=...
Origin: https://claude.ai
Referer: https://claude.ai/
User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36
sec-ch-ua: "Not(A:Brand";v="99", "Chromium";v="133", "Google Chrome";v="133"
sec-ch-ua-mobile: ?0
sec-ch-ua-platform: "Linux"
sec-fetch-dest: empty
sec-fetch-mode: cors
sec-fetch-site: same-origin
```

---

## 5. Struktur Payload Request `completion`

```json
{
  "prompt": "Tuliskan puisi singkat tentang alam.",
  "timezone": "Asia/Jakarta",
  "model": "claude-3-7-sonnet-20250219",
  "attachments": [],
  "files": [],
  "rendering_mode": "messages"
}
```

Bila mode **Extended Thinking** (Claude 3.7 Sonnet) diaktifkan:
```json
{
  "prompt": "Selesaikan soal matematika ini...",
  "timezone": "Asia/Jakarta",
  "model": "claude-3-7-sonnet-20250219",
  "thinking": {
    "type": "enabled",
    "budget_tokens": 4096
  }
}
```

---

## 6. Parsing Format Response Stream SSE

Claude Web mengirimkan stream dengan prefix `event: ...` dan `data: ...`:

1. **`event: completion`**
   ```json
   data: {"completion": "Halo", "stop_reason": null}
   ```
2. **`event: completion` (lanjutan)**
   ```json
   data: {"completion": "! Ada yang bisa saya bantu?", "stop_reason": null}
   ```
3. **`event: completion` (selesai)**
   ```json
   data: {"completion": "", "stop_reason": "stop_sequence"}
   ```
4. **`event: ping`**
   ```json
   data: {"ping": true}
   ```

AxonRouter membaca stream tersebut dan memancarkannya kembali ke client dalam format standard OpenAI SSE:
```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"delta":{"content":"Halo"}}]}
```

---

## 7. Model Catalog & Mapping

Pada AxonRouter `internal/providers/registry_models.go`:

| Public Alias / Model ID | Upstream Model Claude Web | Keterangan |
| :--- | :--- | :--- |
| `cweb/claude-3-7-sonnet` | `claude-3-7-sonnet-20250219` | Model flagship terbaru dengan thinking |
| `cweb/claude-3-7-thinking`| `claude-3-7-sonnet-20250219` | Thinking mode aktif (`budget_tokens: 4096`) |
| `cweb/claude-3-5-sonnet` | `claude-3-5-sonnet-20241022` | Model coding andalan |
| `cweb/claude-3-5-haiku`  | `claude-3-5-haiku-20241022`  | Model cepat berlatensi rendah |
| `cweb/claude-web`        | `claude-3-7-sonnet-20250219` | Default alias |

---

## 8. Langkah Integrasi di Sisi AxonRouter-Go

1. **Daftarkan Provider di Registry:**
   - File: `internal/providers/provider_registry.go`
   - Tambahkan entry `claude-web`:
     ```go
     "claude-web": {
         ID:           "claude-web",
         Name:         "Claude Web",
         Alias:        "cweb",
         Category:     CategoryWebCookie,
         AuthType:     "cookie",
         Color:        "#D97706",
         Icon:         "psychology",
         AuthHint:     "Paste your sessionKey cookie value from claude.ai (sk-ant-sid01-...)",
         ServiceKinds: []string{"llm"},
         Website:      "https://claude.ai",
     },
     ```

2. **Tambahkan Executor Claude Web:**
   - Folder: `internal/proxy/executor/`
   - File: `claude_web.go`, `claude_web_client.go`, `claude_web_stream.go`
   - Pola implementasi mengacu pada `gemini_web.go` dan `deepseek_web.go` yang sudah ada.

3. **Routing REST Connection:**
   - Handler dashboard: `POST /api/providers/claude-web/connections`
   - Menerima payload sync cookie langsung dari Chroma.

---

## 9. Langkah Integrasi di Sisi Chroma

1. **Ekstraksi Cookie di Chroma:**
   - Di `src/axonrouter/sync.mjs`:
     Ambil cookie `sessionKey` dari profil Chroma yang pernah membuka `claude.ai`.
2. **Sync ke AxonRouter:**
   - Kirim `sessionKey` dan `User-Agent` ke endpoint `http://127.0.0.1:3177/api/providers/claude-web/connections`.
   - AxonRouter otomatis menerima akun tersebut ke dalam pool round-robin.
