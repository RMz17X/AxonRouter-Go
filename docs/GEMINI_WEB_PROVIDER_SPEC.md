# Gemini Web (`gemini-web`) Upstream Provider Specification & Sync Architecture

> **Dokumen Status:** Production Technical Specification & Integration Guide  
> **Target Codebase:** AxonRouter-Go (`/workspaces/AxonRouter-Go`) & Chroma Daemon (`http://127.0.0.1:8310`)  
> **Terakhir Diperbarui:** 2026-11-11

---

## 1. Ikhtisar & Status Implementasi di Codebase

Berbeda dengan provider berbasis API publik atau OAuth resmi, **`gemini-web`** menjembatani antarmuka konsumen Google Gemini Web (`gemini.google.com`) ke protokol standar OpenAI Chat Completions (`/v1/chat/completions`) dan endpoint media/gambar.

Di dalam codebase **AxonRouter-Go**, provider `gemini-web` telah diimplementasikan secara komprehensif, modular, dan berkinerja tinggi pada package [`internal/proxy/executor/`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/):

| File Komponen | Peran & Tanggung Jawab |
| :--- | :--- |
| [`gemini_web.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web.go) | Forwarder utama chat completions, orchestration turn, verifikasi identitas model. |
| [`gemini_web_payload.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_payload.go) | Konstruksi array protobuf 81-slot untuk endpoint `StreamGenerate`. |
| [`gemini_web_discovery.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_discovery.go) | Model Discovery dinamis via RPC `otAQ7b` Google (mengetahui model Flash, Pro, Argon sesuai tier akun). |
| [`gemini_web_client.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_client.go) | Pooling sesi TLS, resolusi token CSRF `SNlM0e` via `GET /app`, dan cache eviction (`GeminiWebEvictSession`). |
| [`gemini_web_rotate.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_rotate.go) | Internal rotation engine via `accounts.google.com/RotateCookies` untuk memperbarui `__Secure-1PSIDTS`. |
| [`gemini_web_rotation.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_rotation.go) | Background scheduler rotasi sesi periodik dengan proteksi collision detection. |
| [`gemini_web_stealth.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_stealth.go) | Evasion bot detection, TLS fingerprint impersonation via `bogdanfinn/fhttp` / uTLS. |
| [`gemini_web_thinking.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_thinking.go) | Pemetaan multi-level reasoning (`low: 1`, `medium: 4`, `high: 2`, `thinking_budget`). |
| [`gemini_web_tools.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_tools.go) | Emulasi tool / function calling melalui prompt rendering & output stream parsing. |
| [`gemini_web_image.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_image.go) | Upload multimodal attachments dan image generation via Imagen 3. |
| [`gemini_web_quota.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_quota.go) | Deteksi kuota tier, cooldown 1 jam pada HTTP 429 / kuota habis. |

---

## 2. Autentikasi, Cookies & Resolusi Token

### A. Komposisi Cookie Google
Koneksi `gemini-web` menyimpan string cookie sesi pada kolom `apiKey`:
* **`__Secure-1PSID`** *(Wajib)*: Token identitas sesi utama akun Google.
* **`__Secure-1PSIDTS`** *(Wajib)*: Cookie timestamp berkala (diperbarui Google setiap beberapa jam/menit).
* **`__Secure-1PAPISID` / `SAPISID`** *(Wajib)*: Digunakan untuk menghasilkan header otentikasi `SAPISIDHASH` pada request RPC batchexecute.
* **`SID`, `HSID`, `SSID`** *(Pelengkap)*: Identitas pelengkap akun.

### B. Resolusi Otomatis CSRF Token `SNlM0e`
Setiap request ke Google RPC membutuhkan anti-CSRF token `SNlM0e` (field form `at`).
* AxonRouter secara otomatis melakukan `GET https://gemini.google.com/app` membawa header cookie.
* Mengekstrak token melalui regex: `"SNlM0e":"([^"]+)"`.
* Token di-cache dalam struct `geminiWebTokens` di memori sesi.

> **Batasan Buffer HTTP Header (Wajib >= 64 KB):**  
> Google mengirimkan header respon `Set-Cookie` berukuran sangat besar pada `/app`. Buffer pembaca header HTTP pada klien harus minimal **65.536 byte (64 KB)** untuk mencegah kegagalan `header field too large`.

---

## 3. Arsitektur Dual-Mode Pengelolaan Sesi: Standalone vs Chroma Master Sync

Google Gemini Web sangat sensitif terhadap **rotasi ganda**: jika browser Chrome milik pengguna yang sedang aktif merotasi cookie `__Secure-1PSIDTS`, dan pada saat yang sama AxonRouter juga memanggil endpoint `accounts.google.com/RotateCookies`, **kedua proses akan saling membatalkan dan akun Google langsung ter-logout**.

Oleh karena itu, AxonRouter-Go mendukung **dua mode operasional**:

```
+-----------------------------------------------------------------------------------+
| MODE A: STANDALONE (Self-Healing)                                                 |
| Headless / Server-Only (Cookie dari Incognito / Browser Tertutup)                 |
|                                                                                   |
| AxonRouter Background Loop ---> accounts.google.com/RotateCookies                 |
| (Mengelola pembaruan __Secure-1PSIDTS secara mandiri setiap ~9 menit)             |
+-----------------------------------------------------------------------------------+

+-----------------------------------------------------------------------------------+
| MODE B: CHROMA MASTER SYNC (Sangat Direkomendasikan & Paling Efisien)             |
| Browser Profil Pengguna Aktif di Desktop/Local Daemon                             |
|                                                                                   |
|  +--------------------+       Push HTTP PUT /api/connections/:id       +----------------+ |
|  |   CHROMA DAEMON    | ---------------------------------------------> | AXONROUTER-GO  | |
|  | (Chrome CDP Active)|  Data: { apiKey: "...", source: "chroma" }     | (Auto-Bypass   | |
|  +--------------------+                                                |  Rotasi Lokal) | |
|                                                                        +----------------+ |
+-----------------------------------------------------------------------------------+
```

### Deteksi Otomatis & Proteksi Collision
Saat AxonRouter mendeteksi koneksi memiliki konfigurasi berikut di `providerSpecificData`:
```json
{
  "source": "chroma",
  "disableAutoRotation": true
}
```
Maka:
1. **Background Rotation Internal Otomatis Dilewati (Bypassed):**  
   AxonRouter tidak akan melakukan panggilan rotasi mandiri ke Google, sehingga 100% meniadakan risiko benturan rotasi (*race condition*).
2. **Instant Stale Session Eviction:**  
   Saat Chroma mem-push cookie baru via `PUT /api/connections/:id`, AxonRouter langsung mengeksekusi `GeminiWebEvictSession(oldKey)`. Sesi lama dan koneksi TLS idle segera ditutup dan dibebaskan dari memori, mencegah kebocoran memori (*memory leak*) dan koneksi menggantung.

---

## 4. Spesifikasi Kontrak API Sinkronisasi: Chroma -> AxonRouter

### A. Endpoint Pembaruan Koneksi
* **URL:** `PUT /api/connections/{connection_id}` (atau `POST /api/connections` jika pendaftaran baru)
* **Headers:**
  ```http
  Content-Type: application/json
  Authorization: Bearer <ADMIN_OR_API_KEY> (jika dashboard auth aktif)
  ```
* **Payload Request (`PUT`):**
  ```json
  {
    "name": "Gemini Web (Chroma - adityameka65)",
    "provider": "gemini-web",
    "apiKey": "__Secure-1PSID=g.a000...; __Secure-1PSIDTS=sidts-...; __Secure-1PAPISID=...; SAPISID=...; SID=...",
    "isActive": true,
    "providerSpecificData": {
      "source": "chroma",
      "profileId": "adityameka65",
      "syncedAt": 1794343530,
      "disableAutoRotation": true
    }
  }
  ```

### B. Karakteristik Sinkronisasi Efisien
1. **Idempoten:** Pengiriman cookie yang identik tidak memicu re-alokasi klien atau reset state kuota yang tidak perlu.
2. **Frekuensi Sinkronisasi:**
   * **Event-driven:** Pemicuan langsung saat Chroma mendeteksi perubahan cookie `__Secure-1PSIDTS` pada profil pengguna via CDP listener.
   * **Periodic Fallback:** Interval timer setiap 30–60 menit.
   * **Manual Action:** Tombol UI di Chroma Dashboard: `[⚡ Sync Cookie ke AxonRouter]`.

---

## 5. Protokol Upstream Google (`StreamGenerate` & `otAQ7b`)

### A. Endpoint `StreamGenerate` (Chat & Media)
- **URL:**  
  `https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate`
- **Method:** `POST`
- **Content-Type:** `application/x-www-form-urlencoded;charset=utf-8`
- **URL Parameters:**
  - `bl`: Build label (misal: `boq_assistant-bard-web-server_2026...` diekstrak dari `/app`)
  - `_reqid`: Integer acak 5 digit
  - `rt`: `c`
  - `hl`: `en`
- **Form Body:**
  - `at`: `<SNlM0e_token>`
  - `f.req`: `[null, "<serialized_inner_json_array>"]`

### B. Struktur Inner Array 81-Slot
```javascript
const inner = new Array(81).fill(null);

// Slot 0: Prompt & Multimodal attachments
// [promptText, 0, null, [attachmentSlots], null, null, 0]
inner[0] = [promptText, 0, null, attachmentsSlot, null, null, 0];

inner[1] = ["en"];                                     // Slot 1: Language
inner[2] = ["", "", "", null, null, null, null, null, null, ""]; // Slot 2: Continuation metadata
inner[6] = [1];
inner[7] = 1;                                          // Slot 7: Token streaming flag
inner[10] = 1;
inner[11] = 0;                                         // Slot 11: Strip consumer extensions (Maps, Workspace)
inner[17] = [[0]];
inner[18] = 0;
inner[27] = 1;                                         // Slot 27: Early routing return
inner[30] = [4];
inner[41] = [1];
inner[53] = 0;
inner[59] = requestUUID;                               // Slot 59: Request UUID (cocok dengan header x-goog-ext-*)
inner[61] = [];
inner[68] = 1;

// Slot 79: Model Number (Dihasilkan dari Dynamic Model Discovery otAQ7b)
// 1 = Flash-Lite / Default, 2 = Flash, 3 = Pro
inner[79] = resolvedModelNumber;

// Slot 80: Reasoning / Thinking Level
// 1 = Low (Standar), 4 = Medium, 2 = High (Extended)
inner[80] = resolvedThinkingLevel;
```

### C. Dynamic Model Discovery (`otAQ7b`)
Alih-alih berasumsi statis terhadap model akun:
1. AxonRouter memanggil RPC `otAQ7b` via endpoint batchexecute Google.
2. Membawa otentikasi `SAPISIDHASH` pada header `Authorization`.
3. Membaca envelope array 19-slot di mana slot 15 memuat daftar model yang diizinkan untuk tier akun tersebut (Flash-Lite, Flash, Pro, Argon).
4. Hasil penemuan di-cache per sesi dengan TTL aman.

### D. Zero-Allocation Line Streaming & Response Parsing
* Response dari Google memiliki pembuka `)]}'\n`.
* AxonRouter memotong baris pembuka dan mengalirkan chunk menggunakan `bufio.Scanner` baris per baris.
* Setiap blok frame JSON yang membawa teks kandidat (`payload[4][0][1]`) langsung dikonversi menjadi format SSE OpenAI:
  ```json
  data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"delta":{"content":"..."}}]}
  ```
* Tanpa buffering seluruh respon di memori, menjamin penggunaan memori sangat rendah (< 10 MB per stream aktif) sesuai standar Small Hardware (§4.G AGENTS.md).

---

## 6. Penanganan Kuota & Cooldown
Jika respon Google mengembalikan kode status HTTP 429 atau pesan mengandung indikasi kuota habis:
```regex
/rate\s*limit|quota\s*exceeded|resource_exhausted|limit\s*reached|reached\s*(?:your|the)?\s*(?:daily\s*)?limit|too\s*many\s*requests|coba\s*lagi\s*nanti|batas(?:an)?[\s\w]*tercapai/i
```
AxonRouter menandai sesi dan koneksi sebagai `RATE_LIMITED` dan memberlakukan cooldown 1 jam secara deterministik sebelum mencoba kembali, secara mulus mengalihkan request berikutnya ke koneksi cadangan di rotation pool.

---

## 7. Arsitektur & Panduan Implementasi Image Generation (`/v1/images/generations`)

### A. Analisis Status Saat Ini
1. **Vision / Multimodal Input (Menganalisis Gambar):**
   * **Status: 100% SUKSES.**
   * Ditangani oleh [`internal/proxy/executor/gemini_web_image.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_image.go). Gambar diunggah ke Google push service (`https://content-push.googleapis.com/upload`) dengan tenant `bard-storage`, kemudian disematkan pada array slot 0 request chat.
2. **Image Generation (Pembuatan Gambar Baru via Imagen 3):**
   * **Status: 100% SUKSES (Implemented & Verified).**
   * Ditangani oleh [`internal/handlers/media/gemini_web.go`](file:///workspaces/AxonRouter-Go/internal/handlers/media/gemini_web.go) dan [`internal/proxy/executor/gemini_web_image_gen.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_image_gen.go).
   * Mendukung panggilan langsung ke `/v1/images/generations` (`imagen-3`, `imagen-3.0-generate-002`, `gweb/imagen-3`), Combo model fallback chaining, serta Image Edits (`/v1/images/edits`) dengan input gambar referensi.
   * Model mengalirkan turn Imagen 3 dan mengunduh asset gambar menggunakan sesi Google berotentikasi (uTLS + cookies) untuk mencegah error 401/403/404 CDN Google, lalu mengembalikan payload standar OpenAI `{ "created": ..., "data": [{"b64_json": "..."}] }`.

---

### B. Blueprint Implementasi (Mengikuti Pola `grok-web`)

AxonRouter-Go telah memiliki pola baku untuk provider web headless yang menghasilkan media, yaitu pada [`internal/handlers/media/grok_web.go`](file:///workspaces/AxonRouter-Go/internal/handlers/media/grok_web.go) (`useExecutor: true`). Implementasi untuk `gemini-web` mengikuti pola yang sama persis:

```
+-------------------------------------------------------------------------------+
|                     Klien OpenAI SDK (/v1/images/generations)                 |
+---------------------------------------|---------------------------------------+
                                        | POST {"prompt": "...", "model": "..."}
                                        v
+-------------------------------------------------------------------------------+
| media.HandleImages -> internal/handlers/media/media.go                        |
|                                                                               |
|   if (endpoint == "/v1/images/generations") &&                                |
|      (modelInfo.Provider == "gemini-web" || modelInfo.Provider == "gweb") {   |
|       return h.handleGeminiWebImage(w, r, body, modelInfo)                    |
|   }                                                                           |
+---------------------------------------|---------------------------------------+
                                        |
                                        v
+-------------------------------------------------------------------------------+
| internal/handlers/media/gemini_web.go (Handler)                               |
|                                                                               |
|   1. Parse prompt & ukuran                                                    |
|   2. Ambil koneksi aktif via GetBestConnectionWithContext                     |
|   3. Panggil executor.GenerateGeminiWebImage(ctx, session, prompt)            |
|   4. Format output OpenAI {"created": ..., "data": [{"b64_json": "..."}]}     |
+---------------------------------------|---------------------------------------+
                                        |
                                        v
+-------------------------------------------------------------------------------+
| internal/proxy/executor/gemini_web_image_gen.go (Eksekutor)                   |
|                                                                               |
|   1. Kirim turn ke StreamGenerate: "Generate an image of " + prompt           |
|   2. Ambil URL stub dari respon: googleusercontent.com/image_generation_content|
|   3. Download byte gambar menggunakan TLS Session & Cookies akun Google yang  |
|      sama (agar tidak 404/401)                                                |
|   4. Encode byte gambar ke Base64 (b64_json)                                  |
+-------------------------------------------------------------------------------+
```

---

### C. File-File yang Disentuh & Contoh Kode

#### 1. Tambahkan Routing di [`internal/handlers/media/media.go`](file:///workspaces/AxonRouter-Go/internal/handlers/media/media.go)
Sekitar baris 1150 (berdampingan dengan `grok-web`):
```go
// Gemini Web Imagen 3: endpoint media /v1/images/generations
if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") &&
    (modelInfo.Provider == "gemini-web" || modelInfo.Provider == "gweb") {
    if err := h.handleGeminiWebImage(w, r, body, modelInfo); err != nil {
        handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
    }
    return
}
```

#### 2. Buat Handler Baru: [`internal/handlers/media/gemini_web.go`](file:///workspaces/AxonRouter-Go/internal/handlers/media/gemini_web.go)
```go
package media

import (
    "fmt"
    "net/http"
    "strings"
    "time"

    json "encoding/json/v2"

    "axonrouter/proxy/internal/handlers/chat"
    "axonrouter/proxy/internal/proxy/executor"
)

// handleGeminiWebImage menangani endpoint /v1/images/generations untuk provider gemini-web.
func (h *MediaHandler) handleGeminiWebImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
    var req struct {
        Prompt         string `json:"prompt"`
        Model          string `json:"model"`
        ResponseFormat string `json:"response_format"` // "b64_json" atau "url"
    }
    _ = json.Unmarshal(body, &req)
    prompt := strings.TrimSpace(req.Prompt)
    if prompt == "" {
        return fmt.Errorf("missing required field: prompt")
    }

    // Ambil koneksi Google aktif dengan sesi cookies valid
    conn, _, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "gemini-web", modelInfo.ConnectionID, nil, modelInfo.Model)
    if err != nil {
        return fmt.Errorf("gemini-web: no active Google connection available: %w", err)
    }

    // Panggil eksekutor untuk membuat dan mengunduh gambar
    b64Image, err := executor.GenerateGeminiWebImage(r.Context(), conn, prompt)
    if err != nil {
        return err
    }

    // Kembalikan format standar OpenAI Images API
    data := []map[string]any{
        {
            "b64_json":       b64Image,
            "revised_prompt": prompt,
        },
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    return json.NewEncoder(w).Encode(map[string]any{
        "created": time.Now().Unix(),
        "data":    data,
    })
}
```

#### 3. Buat Fungsi Eksekutor: [`internal/proxy/executor/gemini_web_image_gen.go`](file:///workspaces/AxonRouter-Go/internal/proxy/executor/gemini_web_image_gen.go)
```go
package executor

import (
    "context"
    "encoding/base64"
    "fmt"
    "io"
    "net/http"
    "regexp"
    "strings"

    "axonrouter/proxy/internal/db"
)

var imagenURLPattern = regexp.MustCompile(`https?://[^\s"'\\]*(?:googleusercontent\.com|gstatic\.com)[^\s"'\\]*`)

// GenerateGeminiWebImage mengirim prompt pembuatan gambar ke Google Imagen 3,
// mengekstrak URL hasil, dan mengunduh aset gambar dengan cookies sesi yang sama.
func GenerateGeminiWebImage(ctx context.Context, conn *db.ProviderConnection, prompt string) (string, error) {
    session := geminiWebSessionForConn(conn)
    if session == nil {
        return "", fmt.Errorf("gemini-web: failed to establish authenticated TLS session")
    }

    enrichedPrompt := fmt.Sprintf("Generate an image of %s, high quality photographic detail", prompt)
    turnReq := &geminiWebTurnRequest{
        Prompt:      enrichedPrompt,
        ModelNumber: 1, // Standar Flash / Web image generator
    }

    // 1. Eksekusi turn RPC StreamGenerate
    respText, err := session.executeTurn(ctx, turnReq)
    if err != nil {
        return "", fmt.Errorf("gemini-web image generation failed: %w", err)
    }

    // 2. Cari URL gambar pada respon
    match := imagenURLPattern.FindString(respText)
    if match == "" {
        return "", fmt.Errorf("gemini-web: model did not return a valid Imagen URL (response: %s)", respText)
    }

    // 3. Unduh gambar menggunakan client TLS yang membawa header Cookie Google
    getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, match, nil)
    if err != nil {
        return "", err
    }
    getReq.Header.Set("User-Agent", geminiWebUserAgent)
    getReq.Header.Set("Cookie", conn.APIKey)
    getReq.Header.Set("Referer", "https://gemini.google.com/")

    resp, err := session.client.Do(getReq)
    if err != nil {
        return "", fmt.Errorf("gemini-web: failed to download generated image: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("gemini-web: download image answered HTTP %d", resp.StatusCode)
    }

    imgBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", fmt.Errorf("gemini-web: read image bytes failed: %w", err)
    }

    // 4. Return string base64 untuk b64_json
    return base64.StdEncoding.EncodeToString(imgBytes), nil
}
```

#### 4. Registrasi Model di [`internal/providers/registry_models.go`](file:///workspaces/AxonRouter-Go/internal/providers/registry_models.go)
Pastikan model `imagen-3` terdaftar pada provider `gemini-web`:
```go
"gemini-web": {
    "gemini-web",
    "gemini-web-advanced",
    "gemini-web-thinking",
    "imagen-3", // <-- Model khusus image generation
    "gemini-3.1-pro",
    "gemini-3.1-flash-lite",
    "gemini-4-argon",
},
```

---

### D. Hasil yang Diharapkan
Setelah implementasi di atas selesai:
1. Permintaan dari aplikasi apa pun yang menggunakan OpenAI Python/Node SDK:
   ```python
   from openai import OpenAI
   client = OpenAI(base_url="http://127.0.0.1:3177/v1", api_key="sk-...")
   
   response = client.images.generate(
       model="gweb/imagen-3", # atau gweb/gemini-3.1-pro
       prompt="A cute neon blue origami robot",
       response_format="b64_json"
   )
   image_base64 = response.data[0].b64_json
   ```
2. AxonRouter akan mengembalikan objek standar `{ "created": ..., "data": [{ "b64_json": "..." }] }` secara instan tanpa error 404 lagi.

