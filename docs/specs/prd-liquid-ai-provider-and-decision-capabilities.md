# PRD: Native Liquid AI Provider with Dual Capabilities (LLM Chat + Decision Engine)

**Project:** AxonRouter-Go  
**Target File:** `docs/specs/prd-liquid-ai-provider-and-decision-capabilities.md`  
**Author:** Petani (Farm Ops) / Ricki  
**Status:** DRAFT / APPROVED FOR IMPLEMENTATION  
**Date:** 2026-11-08  

---

## 1. Executive Summary

Liquid AI mengoperasikan dua arsitektur model komputasi yang berbeda di bawah satu payung autentikasi API key (`liquid_...`):
1. **Liquid Foundation Models (LFM)**: Model LLM generatif berbasis Liquid Neural Networks (LNN / dynamical state-space) dengan kemampuan native reasoning (`lfm2.5-24b-a2b`), lightweight inference (`lfm3-1.7b`), dan vision-language multimodal (`lfm3-vl-2b`). Protokol downstream/upstream: **OpenAI Chat Completions**.
2. **System One Decision Models (`d1`, `d1.1`, `d1-ultra`)**: Model klasifikasi, skoring, reranking, dan gatekeeper probabilistik zero-output-token berkecepatan tinggi (<200ms) dengan biaya komputasi sangat murah ($0.042 / 1M input tokens). Protokol downstream/upstream: **System One Decision Primitive (`/v1/systemone`)**.

Saat ini di cluster produksi DewaCloud (`cloud-pg` DB `axonrouter`), Liquid AI terdaftar secara keliru sebagai node kustom generic `openai-compatible-chat-liquid` dengan `baseUrl: https://console.liquid.ai/decisions/v1`. Akibatnya:
- Request OpenAI chat completion mengalami **HTTP 404 Not Found**.
- Pool **2.703 API key aktif** yang sudah dipanen menjadi idle dan tidak bisa dimanfaatkan oleh user/agen.

PRD ini merumuskan pengangkatan Liquid AI menjadi **First-Class Native Provider (`liquid`)** di AxonRouter-Go dengan arsitektur **Dual-Capability (`llm` + `systemone`)**.

---

## 2. Problem Statement & Root Cause

1. **Root Cause 404 Saat Ini**:
   - URL `https://console.liquid.ai/decisions/v1` **hanya** menerima format System One (`/systemone`), bukan `/chat/completions`.
   - Endpoint OpenAI Chat resmi Liquid AI berada di `https://api.liquid.ai/openai/v1` (atau `https://console.liquid.ai/openai/v1`).
2. **Keterbatasan Format Generic OpenAI-Compatible**:
   - Jika hanya didaftarkan sebagai `openai-compatible`, model-model decision `d1` tidak bisa dipakai karena ditolak oleh executor chat completions standar.
   - Jika hanya didaftarkan sebagai `systemone`, model-model LLM LFM (`lfm2.5-24b-a2b`, `lfm3-vl-2b`) tidak bisa diakses oleh aplikasi chat/coding standar.
3. **Solusi Arsitektural**:
   - Satukan kedua kapabilitas di dalam satu provider id: `liquid`.
   - Satu pool API key dapat melayani request `/v1/chat/completions` DAN `/v1/systemone` secara bersamaan tanpa duplikasi entri akun di database.

---

## 3. Product Architecture & Dual-Lane Topology

```
                                  ┌───────────────────────────────┐
                                  │      Client / OpenBot / UI     │
                                  └───────────────┬───────────────┘
                                                  │
                                                  ▼
                                 ┌─────────────────────────────────┐
                                 │       AxonRouter-Go Core        │
                                 │   (Port 3177 / Production)      │
                                 └────────┬───────────────┬────────┘
                                          │               │
     POST /v1/chat/completions            │               │  POST /v1/systemone
     (Model: liquid/lfm2.5-24b-a2b, etc)  │               │  (Model: liquid/d1-ultra, etc)
                                          ▼               ▼
                       ┌──────────────────────┐       ┌──────────────────────┐
                       │ Chat/Media Forwarder │       │  SystemOne Forwarder │
                       │ (internal/handlers)  │       │ (internal/handlers)  │
                       └──────────┬───────────┘       └──────────┬───────────┘
                                  │                              │
                                  └──────────────┬───────────────┘
                                                 │
                                                 ▼
                               ┌──────────────────────────────────┐
                               │   Connection Pool & Key Leaser   │
                               │     (Provider: "liquid")         │
                               │   2,703+ Active Keys in DB       │
                               └─────────────────┬────────────────┘
                                                 │
                                                 ▼
             ┌───────────────────────────────────┴───────────────────────────────────┐
             │ Upstream Liquid AI Gateway (Bearer liquid_...)                        │
             ├───────────────────────────────────┬───────────────────────────────────┤
             │ [Lane A: OpenAI Chat]             │ [Lane B: Decisions Engine]        │
             │ https://api.liquid.ai/openai/v1/  │ https://api.liquid.ai/decisions/  │
             │ chat/completions                  │ v1/systemone                      │
             └───────────────────────────────────┴───────────────────────────────────┘
```

---

## 4. Technical Specification (AxonRouter-Go)

### 4.1. Backend Go Engine

#### A. Provider Configuration (`internal/providers/providers.go`)
Tambahkan entri `liquid` ke dalam `KnownProviders`:
```go
"liquid": {
    BaseURL:      "https://api.liquid.ai/openai/v1/chat/completions",
    AuthHeader:   "Authorization",
    AuthScheme:   "bearer",
    SystemoneURL: "https://api.liquid.ai/decisions/v1/systemone",
},
```

#### B. Provider Registry Metadata (`internal/providers/provider_registry.go`)
Definisikan entri registri `liquid`:
```go
"liquid": {
    ID:           "liquid",
    Name:         "Liquid AI",
    Alias:        "liquid",
    Category:     CategoryAPIKey,
    AuthType:     "apikey",
    Color:        "#00D2B4",
    Icon:         "psychology",
    Priority:     40,
    ServiceKinds: []string{"llm", "systemone"},
    Website:      "https://liquid.ai",
    Notice: &Notice{
        Text:      "Liquid Foundation Models (LFM) with native reasoning & multimodal support, plus System One calibrated decision models.",
        APIKeyURL: "https://console.liquid.ai/api-keys",
        SignupURL: "https://console.liquid.ai/sign-up",
    },
},
```

#### C. Model Catalog (`internal/providers/registry_models.go`)
Petakan model-model resmi Liquid AI dan jenis layanannya:
```go
// Di ProviderModels
"liquid": {
    "lfm2.5-24b-a2b": "llm",
    "lfm3-1.7b":      "llm",
    "lfm3-vl-2b":     "llm",
    "d1":             "systemone",
    "d1.1":           "systemone",
    "d1-ultra":       "systemone",
},
```
Serta daftarkan model default di slice `ProviderModelsList`:
```go
"liquid": {"lfm2.5-24b-a2b", "lfm3-1.7b", "lfm3-vl-2b", "d1", "d1.1", "d1-ultra"},
```

#### D. Model Capabilities & Reasoning Support (`internal/providers/capabilities.go`)
- `lfm2.5-24b-a2b`:
  - `Reasoning: true` (memancarkan blok `reasoning_content` di respon OpenAI).
  - `ContextWindow: 32768`, `MaxOutput: 8192`.
- `lfm3-vl-2b`:
  - `Vision: true`, `ImageInput: true`.
  - `ContextWindow: 131072`, `MaxOutput: 4096`.
- `lfm3-1.7b`:
  - `ContextWindow: 32768`, `MaxOutput: 4096`.
- `d1`, `d1.1`, `d1-ultra`:
  - `Decision: true` / `SystemOne: true`.

#### E. Key Validation Probe (`internal/handlers/dashboard/validate.go`)
Buat validator ringan untuk API key Liquid AI tanpa memotong kuota/saldo:
```go
case "liquid":
    headers := validateAuthHeaders(cfg, apiKey)
    status, _, err := validateProbeDo(ctx, http.MethodGet, "https://api.liquid.ai/openai/v1/models", headers, nil)
    if err != nil {
        return validateOutcome{supported: true, message: err.Error()}
    }
    if status == http.StatusUnauthorized || status == http.StatusForbidden {
        return validateOutcome{supported: true, message: "Invalid API key"}
    }
    if status < 200 || status >= 300 {
        return validateOutcome{supported: true, message: fmt.Sprintf("Liquid API error (%d)", status)}
    }
    return validateOutcome{valid: true, supported: true}
```

---

### 4.2. Web Frontend (Vite / React / TypeScript)

#### A. Provider Catalog (`web/src/lib/providers.ts`)
Tambahkan entri katalog UI:
```ts
{
  id: 'liquid',
  name: 'Liquid AI',
  category: 'api_key',
  color: '#00D2B4',
  icon: 'psychology',
  authType: 'apikey',
  serviceKinds: ['llm', 'systemone'],
  systemoneConfig: {
    baseUrl: 'https://api.liquid.ai/decisions/v1',
  },
  website: 'https://liquid.ai',
  notice: {
    text: 'Liquid Foundation Models (LFM with native reasoning) & System One Decision Models.',
    apiKeyUrl: 'https://console.liquid.ai/api-keys',
    signupUrl: 'https://console.liquid.ai/sign-up',
  },
}
```

#### B. Model List & Capabilities UI (`web/src/lib/models.ts`)
Definisikan model default dan badging modalitas (`Reasoning`, `Vision`, `Decision`):
- `lfm2.5-24b-a2b`: Badges: `[LLM, REASONING]`.
- `lfm3-vl-2b`: Badges: `[LLM, VISION]`.
- `lfm3-1.7b`: Badges: `[LLM, FAST]`.
- `d1`, `d1.1`, `d1-ultra`: Badges: `[DECISION, SYSTEM_ONE]`.

---

## 5. Use Cases & Value Proposition

1. **High-Performance Reasoning LLM (`lfm2.5-24b-a2b`)**:
   - Alternatif berbiaya nol (menggunakan farm account pool) untuk task penalaran, code review, dan sintesis instruksi.
2. **Autonomous Intent Router & Smart Gate**:
   - Memanfaatkan model `d1` via `/v1/systemone` di dalam sistem pipeline OpenBot untuk mengklasifikasikan prompt (apakah coding, farm ops, pentest, atau percakapan biasa) dalam <150ms sebelum dikirim ke agent spesialis.
3. **Safety & Policy Gating**:
   - Menyaring input prompt sensitif menggunakan primitive evaluasi probabilistik `noul` tanpa mengonsumsi token generasi model besar.

---

## 6. Migration & Database Alignment Plan

1. **Database Update di DewaCloud `cloud-pg`**:
   - Ubah `provider` pada 2.703 baris di tabel `provider_connections` dari:
     `openai-compatible-chat-liquid` -> `liquid`.
   - Update konfigurasi koneksi agar diarahkan ke provider native `liquid`.
2. **Testing Gate (hijilaptop -> DewaCloud)**:
   - Build dan jalankan unit test Go di `hijilaptop`:
     `go test ./internal/providers/... ./internal/handlers/media/... -run "TestLiquid"`
   - Verifikasi downstream `/v1/chat/completions` (model `lfm2.5-24b-a2b`) dan `/v1/systemone` (model `d1`).
   - Push commit ke `main` -> GitHub Actions build GHCR -> Watchtower sync ke production node 80760.
