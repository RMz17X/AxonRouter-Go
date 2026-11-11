# KIMI-WEB PROVIDER SPECIFICATION & IMPLEMENTATION BLUEPRINT (AxonRouter-Go)

Dokumen ini mendefinisikan arsitektur dan panduan implementasi untuk menambahkan provider **`kimi-web`** ke dalam **AxonRouter-Go**, di mana **Chroma** bertindak sebagai *Identity & Session Vault* (mengekstraksi `refresh_token` dari Local Storage browser), dan **AxonRouter-Go** bertindak sebagai *LLM Router & Translation Gateway* (OpenAI-compatible `/v1/chat/completions`).

---

## 1. Arsitektur & Pembagian Tugas

```
┌────────────────────────────────────────────────────────┐
│                        CHROMA                          │
│  - Browser Profiler (Fingerprint, Proxy, Canvas, WebGL)│
│  - User Login ke kimi.moonshot.cn di browser asli      │
│  - Ekstraksi Local Storage: refresh_token              │
│  - Push Sync ke AxonRouter via REST API                │
└──────────────────────────┬─────────────────────────────┘
                           │ POST /api/providers/kimi-web/connections
                           ▼
┌────────────────────────────────────────────────────────┐
│                   AXONROUTER-GO                        │
│  - Provider: kimi-web (CategoryWebCookie)              │
│  - Auto-Refresh Access Token (via refresh_token)       │
│  - Connection Pool (Multi-Akun Kimi, failover, quota)  │
│  - Protocol Translator (OpenAI -> Kimi Web SSE Stream) │
│  - Upstream Engine: https://kimi.moonshot.cn/api       │
│  - Output: /v1/chat/completions (Context hingga 2M)    │
└────────────────────────────────────────────────────────┘
```

---

## 2. Parameter Kredensial & Autentikasi

### 2.1 Format Data Koneksi
Setiap koneksi `kimi-web` di AxonRouter disimpan dalam tabel PostgreSQL `providerConnections`:
- **`provider`**: `"kimi-web"`
- **`authType`**: `"token"` (atau `"cookie"`)
- **`data`** (JSON / JSONB payload):
  ```json
  {
    "refreshToken": "eyJhbGciOi...",
    "accessToken": "eyJhbGciOi...",
    "tokenExpiresAt": 1794350000,
    "userAgent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
    "proxyUrl": "http://user:pass@host:port"
  }
  ```

### 2.2 Aturan Token & Siklus Hidup
1. **`refreshToken` (Wajib):** Diambil dari `localStorage.getItem("refresh_token")` pada situs `https://kimi.moonshot.cn`. Token ini berumur panjang (mingguan/bulanan).
2. **`accessToken` (Ephemeral / Auto-Renew):** Berumur pendek (~15–30 menit). AxonRouter akan otomatis melakukan refresh token sebelum mengirim request percakapan jika token sudah kedaluwarsa.

---

## 3. Upstream Endpoints `kimi.moonshot.cn`

Base URL: `https://kimi.moonshot.cn` (atau `https://kimi.ai`)

| Aksi | Method | Endpoint | Keterangan |
| :--- | :--- | :--- | :--- |
| **Refresh Access Token** | `GET` | `/api/auth/token/refresh` | Mengubah `refreshToken` menjadi `accessToken` baru. Header: `Authorization: Bearer <refreshToken>`. |
| **Create Chat** | `POST` | `/api/chat` | Menginisialisasi session chat baru. Body: `{"name":"未命名会话","is_example":false}` ➔ Return: `{"id":"<chat_id>"}`. |
| **Stream Completion** | `POST` | `/api/chat/{chat_id}/completion/stream` | Mengirim pesan pengguna dan menerima balasan SSE stream. |
| **Delete Chat** | `DELETE` | `/api/chat/{chat_id}` | Membersihkan sesi chat setelah selesai agar history tidak menumpuk. |

---

## 4. Header Wajib Upstream

Upstream Kimi Web membutuhkan header browser yang konsisten:
```http
Accept: application/json, text/plain, */*
Accept-Language: zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7
Authorization: Bearer <accessToken>
Content-Type: application/json
Origin: https://kimi.moonshot.cn
Referer: https://kimi.moonshot.cn/
User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36
sec-ch-ua: "Not(A:Brand";v="99", "Chromium";v="133", "Google Chrome";v="133"
sec-ch-ua-mobile: ?0
sec-ch-ua-platform: "Linux"
sec-fetch-dest: empty
sec-fetch-mode: cors
sec-fetch-site: same-origin
```

---

## 5. Alur Request Chat Completion (`/stream`)

### 5.1 Step 1: Token Refresh (Bila Access Token Expired)
```http
GET /api/auth/token/refresh HTTP/1.1
Host: kimi.moonshot.cn
Authorization: Bearer <refreshToken>
```
Response:
```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "eyJhbGciOi...",
  "expires_in": 1800
}
```

### 5.2 Step 2: Buat Sesi Chat
```http
POST /api/chat HTTP/1.1
Host: kimi.moonshot.cn
Authorization: Bearer <accessToken>
Content-Type: application/json

{"name":"未命名会话","is_example":false}
```
Response:
```json
{
  "id": "cn4xxxxxxxxxxxxxxxxx"
}
```

### 5.3 Step 3: Kirim Prompt & Terima Stream SSE
```http
POST /api/chat/cn4xxxxxxxxxxxxxxxxx/completion/stream HTTP/1.1
Host: kimi.moonshot.cn
Authorization: Bearer <accessToken>
Content-Type: application/json

{
  "messages": [
    {
      "role": "user",
      "content": "Jelaskan sejarah singkat internet."
    }
  ],
  "refs": [],
  "use_search": true
}
```

---

## 6. Parsing Format Response Stream SSE Kimi

Kimi Web menggunakan format SSE berikut — **bentuk asli yang tertangkap live 2026-11-11** (versi awal bagian ini menebak field `type`; server sebenarnya memakai field `event`):

1. **Event furniture di awal giliran** — BUKAN konten, jangan pernah diteruskan ke client:
   ```json
   data: {"content": "<prompt user>", "event": "req", "model": "kimi"}
   data: {"event": "ping"}
   data: {"event": "resp", "model": "kimi"}
   data: {"event": "zone_set", "idx_z": 0, "zone_type": "normal"}
   data: {"event": "loading", "loading": true, "type": "default"}
   data: {"event": "rename", "text": "<judul chat = kepala prompt, terpotong>"}
   ```
   Khusus `rename`: field `text`-nya adalah judul chat yang dibuat otomatis dari pesan pertama. Memasukkannya ke konten = membocorkan kepala prompt ke jawaban.
2. **`event: cmpl` (Delta teks konten)**
   ```json
   data: {"event": "cmpl", "idx_s": 0, "idx_z": 0, "text": "Internet", "view": "cmpl"}
   ```
3. **`event: done` (Selesai)**
   ```json
   data: {"event": "done"}
   ```

### Konversi ke Format OpenAI:
AxonRouter membaca event `cmpl`, mengekstrak field `text`, lalu memancarkannya kembali ke client dalam format standard OpenAI SSE:
```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"delta":{"content":"Internet"}}]}
```

Dan saat menerima `event: done`, pancarkan penutup:
```
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
```

---

## 7. Model Catalog & Mapping

Pada AxonRouter `internal/providers/registry_models.go`:

| Public Alias / Model ID | Upstream Model Kimi Web | Keterangan |
| :--- | :--- | :--- |
| `kweb/kimi-latest` | `kimi` | Slot default Kimi Web (Context hingga 2M) |
| `kweb/kimi-k2`     | `k2`   | Slot k2 |
| `kweb/kimi-web`    | `kimi` | Default alias |

Search bukan model chat: pencarian dilayani sebagai media service di
`POST /v1/search` (provider `kimi-web` di halaman Media Providers → Web),
menjalankan completion `use_search: true` dan memetakan event
`search_plus` menjadi hasil terstruktur. Nama publik lama `kimi-search`
dan `kimi-k2.5` dicabut di v1.10.33 (fragment 1121): `kimi-k2.5` menamai
versi yang tidak ada di produk web, dan id upstream yang diterima server
hanya `[kimi k1 k1.5 k2 k1.5-thinking]` (error validasi 422, terverifikasi
live 2026-11-11 di kimi.moonshot.cn dan kimi.com).

---

## 8. Langkah Integrasi di Sisi AxonRouter-Go

1. **Daftarkan Provider di Registry:**
   - File: `internal/providers/provider_registry.go`
   - Tambahkan entry `kimi-web`:
     ```go
     "kimi-web": {
         ID:           "kimi-web",
         Name:         "Kimi Web (Moonshot)",
         Alias:        "kweb",
         Category:     CategoryWebCookie,
         AuthType:     "token",
         Color:        "#2563EB",
         Icon:         "psychology",
         AuthHint:     "Paste your refresh_token value from kimi.moonshot.cn Local Storage",
         ServiceKinds: []string{"llm", "webSearch"},
         Website:      "https://kimi.moonshot.cn",
     },
     ```

2. **Tambahkan Executor Kimi Web:**
   - Folder: `internal/proxy/executor/`
   - File: `kimi_web.go`, `kimi_web_client.go`, `kimi_web_stream.go`
   - Implementasikan fungsi token rotation otomatis yang menukar `refreshToken` saat `accessToken` expired.

3. **Routing REST Connection:**
   - Handler dashboard: `POST /api/providers/kimi-web/connections`
   - Menerima payload sync `refreshToken` langsung dari Chroma.

---

## 9. Langkah Integrasi di Sisi Chroma

1. **Ekstraksi Token di Chroma:**
   - Menggunakan CDP `Runtime.evaluate` atau `DOMStorage` untuk membaca key `refresh_token` dari domain `https://kimi.moonshot.cn`.
2. **Sync ke AxonRouter:**
   - Mengirim `refreshToken` dan `User-Agent` ke endpoint `http://127.0.0.1:3177/api/providers/kimi-web/connections`.

---

## 10. Status Implementasi (2026-11-11 — SHIPPED, staging v1.10.26)

Diimplementasikan penuh di AxonRouter-Go dan sudah live di staging:

- **Registry & katalog:** `internal/providers/provider_registry.go` (`kimi-web`, alias `kweb`, CategoryWebCookie, auth `token`, warna `#2563EB`, ikon `psychology`, ServiceKinds `llm` + `webSearch` — persis Bagian 8), `registry_models.go` (4 model sesuai Bagian 7), `registry_aliases.go` + `aliases.go` (`kweb` ⇄ `kimi-web`), `providers.go`. Ikon artwork: `web/public/providers/kimi-web.png` (salinan `kimi.png`).
- **Executor:** `internal/proxy/executor/kimi_web.go` (orkestrasi + resolusi kredensial + pembangunan messages; system turn dilipat ke user turn pertama karena client web tidak punya kanal system), `kimi_web_client.go` (transport TLS impersonasi Chrome_133 + fallback plain `KIMI_WEB_PLAIN_HTTP=1`), `kimi_web_api.go` (siklus token + create/delete chat), `kimi_web_stream.go` (parser SSE Bagian 6: `cmpl` → content, delta thinking → `reasoning_content`, `all_done` → finish, `ping` diabaikan, event lain yang membawa `text` diperlakukan sebagai content). Terdaftar di `executor/init.go`.
- **Siklus token (Bagian 2.2 & 5.1):** access token ditukar secara lazy — hanya bila cache kosong atau <30 detik menuju expiry; refresh diserialkan per akun (satu mutex) agar burst paralel tidak merotasi token berbarengan; refresh_token hasil rotasi menggantikan yang tersimpan di cache in-process. Batasan yang diketahui: cache token hidup di memori proses; baris koneksi menyimpan refresh_token terakhir yang disync Chroma, jadi bila rotasi upstream membatalkan token lama DAN proses restart, akun dipulihkan lewat re-sync Chroma.
- **Alur chat:** `POST /api/chat` (chat sekali pakai per request, dihapus best-effort setelah turn) → `POST /api/chat/{id}/completion/stream` dengan body `{messages, refs: [], use_search, model}` — field `model` ditambahkan di samping field Bagian 5.3 agar pemetaan katalog Bagian 7 berlaku; `use_search: true` hanya untuk `kimi-search`.
- **Validasi koneksi:** tombol test melakukan pertukaran token sungguhan ke `GET /api/auth/token/refresh` (`internal/handlers/dashboard/validate.go`, case `kimi-web`).
- **Sync Chroma:** `POST /api/providers/kimi-web/connections` live (`internal/handlers/dashboard/kimi_web_sync.go`), body `{name, refreshToken, accessToken, tokenExpiresAt, userAgent, proxyUrl, proxyPoolId, replace}` — diterjemahkan ke jalur tulis koneksi generik yang sama dengan dashboard.
- **Bukti:** host terverifikasi sebelum implementasi (kimi.moonshot.cn resolve; endpoint refresh menjawab 401 tanpa token); katalog + `/v1/models` memuat `kweb/kimi-latest`, `kweb/kimi-search`, `kweb/kimi-k2.5`, `kweb/kimi-web`; ikon HTTP 200; chat dengan refresh token palsu mencapai endpoint refresh asli dan kembali 401 dengan pesan auth yang didesain; test fake-upstream membuktikan refresh-sekali-lalu-reuse, folding system, dan flag search.
- **Belum terbukti:** happy-path chat dengan akun asli (belum ada kredensial Kimi saat implementasi) — pembuktian live dilakukan terpisah.

**Addendum v1.10.33 (2026-11-11, fragment 1121):** happy-path chat terbukti live (12 koneksi fleet; upstream model id diterjemahkan di executor — Bagian 7 versi awal tidak pernah dites live). Katalog chat dijujurkan: `kimi-k2.5` → `kimi-k2`, `kimi-search` dicabut dari katalog chat dan search dipindah ke permukaan media (`POST /v1/search`, adapter `internal/handlers/media/kimi_search.go` + `executor.ExecuteKimiWebSearch`; ServiceKinds provider menjadi `llm` + `webSearch` + `web` agar tampil di Media Providers → Web). Event `search_plus` (msg `get_res`: title/url/snippet/date/site_name/icon) dipetakan ke amplop `SearchResponse` standar. Protokol SSE asli (Bagian 6 yang dikarang) sudah dikoreksi di v1.10.31: event dinamai di field `event`, bukan `type`; `rename` adalah judul chat dan tidak pernah masuk konten.
