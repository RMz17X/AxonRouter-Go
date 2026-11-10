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
