# Audit: port sistem OpenCode free dari `../obf` ke AxonRouter-Go

Tanggal: 2026-11-21 · Status: P1a + P1b **sudah diimplementasikan**; P2/P3 masih analisis.

Sumber pembanding: `/workspaces/obf` (Bun, single-upstream bridge) dan
`/workspaces/AxonRouter-Go` (Go, multi-provider gateway).

Tujuan: tentukan sistem obf mana yang sudah ada di Go, mana yang layak
diport, dan mana yang tidak cocok dengan arsitektur gateway.

---

## 1. Ringkasan

Sebagian besar sistem obf **sudah ada** di AxonRouter-Go, sebagian bahkan
lebih ketat (harness preamble byte-exact, genuine tool set, stealth TLS).
Gap yang tersisa terkonsentrasi pada satu area: **pemakaian KV/prompt cache
upstream** dan **budget output model reasoning**.

| Prioritas | Item | Status di Go |
|---|---|---|
| P1 | `prompt_cache_key` + `store:false` di lane OpenCode free | **selesai** — `opencode_cache.go` |
| P1 | Floor `max_completion_tokens` untuk model reasoning | **selesai** — `opencode_token_budget.go` |
| P2 | Cache key per percakapan (bukan per koneksi) | parsial |
| P2 | Instruksi JSON + coercion fence untuk `response_format` | belum ada di lane OpenCode |
| P3 | Adopsi version floor saat 426 | klasifikasi ada, refresh tidak |
| P3 | Rendezvous hashing + in-flight per egress | parsial |
| Skip | Korelasi 429 model-wide, auto-probe egress | tidak cocok arsitektur |

---

## 2. Yang sudah ada (jangan diport ulang)

| Sistem obf | Implementasi Go | Catatan |
|---|---|---|
| TLS fingerprint Bun/BoringSSL | `internal/proxy/executor/opencode_stealth.go` | ClientHello 17 cipher + 12 extension, header order |
| Session id `ses_` + descending time | `internal/proxy/opencode.go:29-77` | Termasuk rotasi counter dan re-encode id asing |
| Empat header sesi satu nilai + UA + `x-opencode-client` | `internal/proxy/opencode.go:270-348` | `x-opencode-session`, `-session-id`, `x-session-affinity`, `x-session-id` |
| `<env>` + `Working directory:` | `internal/translator/opencode_env_marker.go:9-13` | Prefix `/tmp/ocprobe` |
| Harness preamble + genuine tool set | `internal/translator/opencode_v2_fingerprint.go:8-28` | Lebih ketat: preamble byte-exact, ≥6 built-in, tool caller dideskripsikan |
| `tool_choice: none` dibuang | `opencode_v2_fingerprint.go:296-343` | Juga normalisasi forced choice |
| `stream: true` wajib | `internal/proxy/executor/providers.go:797-824` | Non-stream tetap minta stream lalu dirakit |
| Body deterministik untuk prefix cache | `marshalStable` + `InjectReasoningContent` | Test `opencode_prefix_cache_test.go` |
| Stable session + rotasi saat 429 | `opencode_session.go` + `opencode_retry.go` | TTL 30 mnt, 1024 entri, rotasi 1x |
| Retry refusal 1x lewat direct egress | `opencode_retry.go:78-130` | Bukan loop |
| Park ladder egress 60→600→1200→1800→2400→7200 s | `internal/handlers/chat/opencode_egress.go:96-115` | Identik dengan `obf/lib/proxy/cooldown-policy.js` |
| `X-OpenCode-Gate` verdict | `opencode_egress.go:180-267` | `shape-rejected`, `ip-throttled`, `ip-abuse` |
| Klasifikasi 403/426/429/abuse/region | `opencode_egress.go` + `fallback.go` | 426 belum di-adopt, hanya diklasifikasi |
| Hedging multi-akun | `internal/handlers/chat/hedging.go` | Staggered start, admission timeout |
| Repair tool-call/tool-result | `internal/translator/request.go:249-312`, `handlers/chat/tool_repair.go` | Kebijakan beda (insert missing + demote orphan) |
| Session affinity lintas akun | `internal/handlers/chat/session_affinity.go` | TTL 2 jam, 10k entri |
| Accounting `cached_tokens` | `internal/db/usage*.go`, `handlers/chat/usage.go` | Cache hit sudah terlihat di analytics |
| Context compaction | `internal/headroom`, `internal/tokensaver` | Berbeda mekanisme, sudah ada |

Kesimpulan: klaim "obf punya banyak sistem" benar, tetapi Go sudah menyerap
hampir seluruh lapisan request-shape, session, egress, dan error contract.

---

## 3. Gap yang layak diport

### P1 — `prompt_cache_key` + `store:false` (KV cache reuse)

**Bukti obf**

- `obf/docs/architecture/OVERVIEW.md:71`: upstream memakai cache prompt lintas
  request; `prompt_cache_key` diambil dari sesi persisten, maksimum 64 karakter.
- `obf/docs/architecture/REQUEST-FLOW.md:30-32`: key = identitas klien + hash
  100 karakter pertama pesan user, dipotong 64.
- `obf/lib/server/messages.js:357-363`: derivasi native CLI —
  `/^ses_[0-9a-f]{64}$/` → `slice(4)`, lalu cap 64.
- `obf/lib/server/messages.js:386-390, 425-430`: untuk chat lane non-`tidepool`,
  set `store:false` dan `prompt_cache_key`.
- `obf/docs/features/SMART-PROXY-ROUTING.md:11`: warm cache memotong prefill
  100K token dari 5–9 s menjadi <1,5 s dan menghemat kuota hingga 90%.
- `obf/lib/server/chat-attempt-core.js:97-99`: key diikat ke sesi per egress.
- `obf/lib/server/chat-attempt-core.js:163`: bila upstream menjawab 400
  `unsupported field`, `prompt_cache_key` dan `store` dihapus lalu retry sekali.

**Kondisi Go**

- `prompt_cache_key` hanya diinjeksi untuk Codex (`executor/providers.go:96-130`)
  dan CommandCode. Lane `ForwardOpencode` tidak pernah mengirimnya.
- `store` hanya dihapus untuk lane yang tidak mendukung; lane OpenCode tidak
  menyentuhnya.
- Session yang dipakai header sudah stabil (`opencodeSessionHint`), tetapi
  nilainya tidak pernah dipakai sebagai cache key.

**Status implementasi (2026-11-21)**

- `internal/proxy/executor/opencode_cache.go`: derivasi key, injeksi dua field,
  klasifikasi 400 (body di posisi response maupun `*proxy.UpstreamError`),
  strip + satu replay.
- Dipasang di `ForwardOpencode` dan `forwardOpencodeGoFree`; sesi di-resolve
  sebelum body dibangun agar `x-opencode-session` dan `prompt_cache_key`
  identik.
- Test: `opencode_cache_test.go` (unit) dan `opencode_cache_wiring_test.go`
  (HTTP end-to-end, termasuk replay setelah 400).

**Rencana awal (dipertahankan untuk konteks)**

1. Di `ForwardOpencode` (chat lane) dan `forwardOpencodeGoFree`, setelah
   `hint := opencodeSessionHint(req, true)`:
   - `session := proxy.TranslateOpenCodeSessionID(hint, "")` — nilai yang sama
     dengan `x-opencode-session`.
   - `reqMap["prompt_cache_key"] = opencodePromptCacheKey(session)`.
   - `reqMap["store"] = false`.
2. Helper `opencodePromptCacheKey` mengikuti CLI: strip `ses_` bila id 64-hex,
   cap 64 karakter.
3. Bila respons 400 memuat `unsupported field`, hapus kedua field dan retry
   **sekali** (jangan loop), mengikuti `chat-attempt-core.js:163`.
4. Jangan menyentuh lane keyed/paid selain free lane yang sudah ada.

**Risiko**

- Sebagian model bisa menolak field tak dikenal → mitigasi strip+retry.
- Key harus stabil antar-turn; `TranslateOpenCodeSessionID` deterministik,
  jadi aman.
- Body tetap harus di-`marshalStable` agar prefix byte-identik.

**Verifikasi**

- Unit: key derivation (canonical 30 char, 64-hex `ses_`, id asing), jalur
  strip pada 400.
- Live: percakapan 3 turn ke model free, bandingkan
  `usage.prompt_tokens_details.cached_tokens` dan latency turn 2–3 sebelum vs
  sesudah. `cached_tokens` sudah tercatat di DB, jadi metriknya siap.

---

### P1 — Floor `max_completion_tokens` untuk model reasoning

**Bukti obf**

`obf/token-budget.js` mengukur langsung di OpenCode Zen:

| `max_tokens` klien | Hasil |
|---|---|
| 50 | kosong (Muse) |
| 100 | kosong (Muse, cat-jam) |
| 256 | kosong (big-pickle); longcat berhenti tepat di cap |
| 512 | ada konten, terpotong |
| 2048 + headroom | jawaban lengkap |

Formula: `max(wanted, 1024) + 4096` untuk model reasoning,
`max(wanted, 512)` untuk non-reasoning, lalu di-clamp ke `limit.output` model.

**Kondisi Go**

- `handlers/chat/normalize_tokens.go` hanya menyatukan spelling
  `max_tokens`/`max_completion_tokens`; tidak menaikkan nilai.
- Tidak ada floor untuk lane OpenCode. Permintaan probe/title dengan
  `max_tokens` kecil bisa berakhir `finish_reason=length` tanpa konten.

**Status implementasi (2026-11-21)**

- `internal/proxy/executor/opencode_token_budget.go`:
  `applyOpenCodeTokenFloor` menulis `max_completion_tokens` dan menghapus
  `max_tokens`.
- Formula: `max(wanted, 1024) + 4096` untuk reasoning, `max(wanted, 512)`
  untuk non-reasoning.
- Ceiling: `GetCapabilitiesForModel(...).MaxOutput` (nilai katalog upstream)
  lebih dulu; substring table hanya dipakai bila nilainya cukup menampung
  floor. Default 4096 untuk id tak dikenal tidak dipakai sebagai ceiling,
  karena akan mengembalikan gejala jawaban kosong yang justru mau dicegah.
  Hard cap 131072 seperti `MAX_OUTPUT_TOKENS` obf.
- Dipasang di `ForwardOpencode` dan `forwardOpencodeGoFree`.
- Test: `opencode_token_budget_test.go`.

**Risiko**

- Menambah cap output tidak menagih token yang tidak dihasilkan; hanya
  mencegah truncation. Tetap batasi ke lane free agar tidak mengubah biaya
  lane paid.
- Model non-reasoning cukup floor 512.

**Verifikasi**

- Unit table-driven: reasoning/non-reasoning, nilai 0/1/50/1024/999999,
  model tanpa katalog.
- Live: `max_tokens: 32` ke model reasoning free → harus mengembalikan konten.

---

### P2 — Cache key per percakapan

**Bukti obf**

- `obf/lib/server/upstream.js:371-410`: key sesi = client key + md5(100 char
  pertama pesan pertama) + hash egress; TTL 3 jam; batas 500 entri.
- `obf/lib/server/chat-attempt-core.js:88-99`: sesi per egress, karena proxy
  merotasi IP per request dan satu sesi lintas exit membuat upstream menghitung
  semua exit sebagai satu device.

**Kondisi Go**

- Tanpa header sesi klien, `StableOpenCodeSessionID` memakai satu sesi per
  koneksi (`opencode:conn:<id>`), TTL 30 menit, 1024 entri
  (`internal/proxy/opencode.go:79-153`).
- Artinya semua percakapan tanpa header berbagi satu sesi dan (setelah P1)
  satu `prompt_cache_key`. Prefix berbeda dengan key sama berpotensi saling
  menimpa cache.

**Rencana implementasi (pilih satu, ukur dulu)**

- Opsi A (aman): pertahankan sesi per koneksi untuk akuntansi kuota, tetapi
  turunkan `prompt_cache_key` dari `hash(session + 100 char pertama pesan user)`.
  Satu sesi tetap, cache lane per percakapan.
- Opsi B (parity obf): ubah key stable session menjadi
  `connection + conversation signature`, sehingga sesi dan cache sama-sama per
  percakapan. Ini menambah jumlah sesi (dan lane kuota) — perlu keputusan
  operator.

Rekomendasi: Opsi A dulu; ukur `cached_tokens`. Jika tidak bergerak, baru
pertimbangkan Opsi B.

---

### P2 — Instruksi JSON + coercion fence

**Bukti obf**

`obf/lib/server/messages.js:300-350`: bila `response_format` JSON, instruksi
"valid JSON only" ditambahkan ke system prompt, dan output dibersihkan dari
fence/prose sebelum dikembalikan.

**Kondisi Go**

- Coercion hanya ada di translator Claude (`claude_request.go:133`) dan Gemini
  (`gemini.go:398`). Lane OpenCode free meneruskan `response_format` apa adanya;
  upstream sering mengabaikannya.

**Rencana**

- Scope ke lane OpenCode free + `response_format` JSON. Tambahkan instruksi ke
  system prompt (setelah `ApplyFingerprintSystemPrompt`, agar preamble tetap
  byte-exact di awal).
- Coercion response hanya untuk non-streaming, atau pada agregasi final.

**Risiko**: mengubah konten response; jangan aktif untuk stream delta.

---

### P3 — Adopsi version floor saat 426

**Bukti obf**

`obf/docs/architecture/REQUEST-FLOW.md` (tabel verdict): 426 → adopsi floor,
refresh identitas, retry sekali. `obf/lib/identity/*` mengambil rilis upstream
dan menurunkan UA/tool profile.

**Kondisi Go**

- UA dipin statis: `DefaultOpenCodeUA = "opencode/latest/2.1.4/cli"`
  (`internal/proxy/opencode.go:18`).
- 426 diklasifikasi (`opencodeClassifiedUpgradeRequired`) tetapi tidak memicu
  refresh; semua request free akan 426 sampai rilis baru.

**Rencana minimal**

- Tambahkan override UA lewat env/DB (mis. `OPENCODE_UA`) agar operator bisa
  menyesuaikan tanpa rebuild.
- Opsional: saat 426, fetch metadata rilis sekali (singleflight, TTL 15 menit)
  dan naikkan versi di memori. Ini lebih besar dan menyentuh jaringan; tunda
  sampai benar-benar terjadi.

---

### P3 — Rendezvous hashing + in-flight per egress

**Bukti obf**

`obf/docs/features/SMART-PROXY-ROUTING.md:2`: session→proxy via rendezvous
hash, spillover ke proxy idle, max 1 request in-flight per IP (upstream free
tier melarang paralel pada IP yang sama).

**Kondisi Go**

- Ada `SessionAffinityStore` (pin sesi ke koneksi) tetapi pemilihan awal
  round-robin/sticky, bukan hash.
- In-flight cap per koneksi hanya untuk Antigravity
  (`handlers/chat/conn_inflight.go`), belum untuk OpenCode.

**Verdict**: hanya relevan bila operator menjalankan banyak akun OpenCode di
belakang satu IP proxy. Ukur dulu 429 konkurensi; jangan tambah kompleksitas
tanpa bukti.

---

### Skip — korelasi 429 model-wide, auto-probe egress, payload archive

- **Korelasi model-wide** (`obf/lib/proxy/429-classify.js`): memarkir egress
  berdasarkan window 429 lintas model. Go sudah punya per-pool strike
  (`opencode_egress.go`) dan breaker `(provider, model)`
  (`handlers/chat/provider_breaker.go`). Menambah ring buffer global berisiko
  memarkir koneksi yang masih sehat.
- **Auto-probe** (`obf/lib/proxy/cooldown-probe.js`): Go hanya punya geo probe
  (`internal/proxy/poolstate/geo.go`). Probe model otomatis menambah trafik
  latar pada box 1 vCPU; tidak sepadan.
- **Payload archive** (`obf/lib/server/payload-archive.js`): menyimpan body
  request/response. Go sudah punya usage history + evidence; arsip payload
  menambah risiko privasi dan disk.

---

## 4. Urutan eksekusi yang disarankan

1. ~~**P1a** `prompt_cache_key` + `store:false` + strip/retry pada 400.~~
   Selesai: `internal/proxy/executor/opencode_cache.go`,
   `opencode_cache_test.go`, `opencode_cache_wiring_test.go`.
2. ~~**P1b** Floor `max_completion_tokens`.~~ Selesai:
   `internal/proxy/executor/opencode_token_budget.go` + test.
3. Ukur `cached_tokens` dan latency 3 turn; putuskan P2a (cache key per
   percakapan).

Semua perubahan wajib berada di jalur eksekusi `opencode` (dan
`opencode-go` free lane), tidak boleh masuk routing generik — sesuai
`AGENTS.md` §3.

---

## 5. Verifikasi umum

```bash
go test ./internal/proxy/executor/... -run 'Opencode|OpenCode'
go test ./internal/translator/...
make vet-svelte   # tidak relevan bila tidak menyentuh web/
```

Tidak ada dependency baru, sehingga gerbang portabilitas §4.F tidak berubah.
