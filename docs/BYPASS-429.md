# Operasional 429 dan Batas Etis

Dokumen ini berisi cara membaca 429 di lapangan dan batas tindakan yang dibolehkan.
Penjelasan arsitektur ada di [ARCHITECTURE.md](ARCHITECTURE.md); kontrak respons error
ada di [API.md](API.md).

## Premis

`429 FreeUsageLimitError` dan `403 FreeTierError` terutama adalah **klasifikasi bentuk
request**, bukan pengumuman kuota habis. Upstream menyembunyikan kriteria klasifikasinya
di balik pesan "usage limit". Bukti: pada IP dan menit yang sama, menghapus `tools` atau
`stream` mengubah 200 menjadi 429.

Empat lapis yang diperiksa:

| Lapis | Syarat | Tempat |
|---|---|---|
| L1 body | `stream: true`, `messages` terisi, `tools` non-empty + tool anchor; `<env>` di `messages[0]` | klien → gateway |
| L2 header | UA `opencode/<channel>/<versi>/<client>`, `x-opencode-client: cli`, header sesi satu nilai | klien → gateway |
| L3 sesi | `ses_` + 12 hex + sisa base62, panjang sesuai resep rilis | runtime klien |
| L4 transport | TLS ClientHello runtime Bun | runtime klien |

Gateway hanya meneruskan L1/L2 dan melaporkan hasil klasifikasi; L3/L4 berada di klien
dan tidak dapat diperbaiki dengan mengganti egress.

## Penanda gate

Untuk penolakan free-tier opencode, gateway menyertakan header `X-OpenCode-Gate`:

| Nilai | Arti |
|---|---|
| `shape-rejected` | bentuk request atau wilayah ditolak — perbaiki L1/L2, bukan IP |
| `ip-throttled` | allowance egress habis — parkir egress dan rotasi ke `fallbacks` |
| `ip-abuse` | egress ditandai upstream sebagai penyalahgunaan |

`426`/upgrade-required adalah *version floor*, bukan verdict gate: tidak ada header yang
dipublikasikan. Penolakan dari provider selain opencode juga tidak ber-header.

## Tabel eskalasi ringkas

| Strike | Durasi parkir |
|---|---|
| 0 | 60 s (default, override `OPENCODE_EGRESS_PARK_SEC`) |
| 1 | 600 s |
| 2 | 1200 s |
| 3 | 1800 s |
| 4 | 2400 s |
| ≥ 5 | 7200 s (cap) |

Strike naik hanya untuk `429` free-tier opencode, dihitung per pool proxy dan direset
setelah 24 jam tanpa 429. Batas absolut parkir 24 jam. `403` adalah penolakan bentuk:
dilaporkan lewat `X-OpenCode-Gate`, tetapi tidak memarkir pool. Override env hanya
mengubah strike 0. Rotasi lintas pool dalam satu request dibatasi `EGRESS_ROTATE_MAX`
(default 4, maksimal 16).

## Urutan penanganan

1. **Perbaiki bentuk dulu.** Baca penanda gate (`X-OpenCode-Gate`) dan log request.
   Kalau bentuk gagal, rotasi IP hanya memindahkan penolakan ke egress lain.
2. **Cek status egress.** Kalau bentuk benar tetapi 429 tetap datang, itu allowance:
   parkir egress, rotasi ke `fallbacks`, replay dengan batas `EGRESS_ROTATE_MAX`.
3. **Tunggu cooldown.** Jangan retry buta saat semua egress diparkir; gateway sudah
   membatasi jumlah rotasi, tetapi tetap menghindari replay yang tidak perlu.
4. **Ganti model** bila satu model tertolak sementara model lain lolos — itu kapasitas model,
   bukan gate.
5. **Satu variabel per percobaan.** Kalau dua hal diubah sekaligus, hasilnya tidak bisa
   disimpulkan.

| Gejala | Jangan | Lakukan |
|---|---|---|
| 429 padahal bentuk lolos | muter session id, menaikkan `max_tokens` | rotasi egress / tunggu cooldown |
| 403 + `X-OpenCode-Gate: shape-rejected` | menyalahkan IP | perbaiki identitas dan tool anchor |
| 403 + runtime bukan Bun | ganti proxy | jalankan dengan Bun 1.4.3 |
| 426 | pin versi lama | adopsi version floor |
| 404/410 rute | replay URL lama | refresh katalog, turunkan URL ulang |
| 400 model tidak tersedia | retry buta | pilih model lain dari `/v1/models` |

## Batas etis dan operasional

1. **Tidak menciptakan kuota.** Gateway membantu lolos klasifikasi dan mengelola allowance
   yang tersedia; ia tidak mengklaim hak akses lebih besar dari yang diberikan upstream.
2. **Tidak membanjiri upstream.** Retry, rotasi, dan parkir semuanya bounded. Jangan
   menaikkan `EGRESS_ROTATE_MAX` atau `OPENCODE_EGRESS_PARK_SEC` untuk "memaksa" respons.
3. **Tidak memalsukan transport lewat proxy MITM.** Proxy harus tunnel `CONNECT`, sehingga
   ClientHello tetap milik runtime lokal. Chaining proxy hanya menambah latensi.
4. **Tidak menyalin jawaban/capture untuk dipakai sebagai resep statis.** Bentuk diambil
   dari rilis upstream dan katalog, supaya perubahan diadopsi, bukan dibekukan.
5. **Tidak menyimpan atau membocorkan kredensial.** Auth proxy dan token tidak masuk log,
   DB, atau dokumentasi; tampilan memakai host termasker.
6. **Tidak menyasar model yang dimatikan upstream** atau mencoba mengabaikan penolakan
   eksplisit. Laporkan apa adanya.
7. **Tidak push/deploy tanpa approval.** Semua perubahan diverifikasi lokal terlebih dahulu.

## Pemeriksaan lapangan

```bash
# Status gateway (PORT default 3177)
curl -s "http://127.0.0.1:${PORT:-3177}/health" | head -c 300; echo

# Verifikasi perilaku gate dan parkir
go test ./internal/handlers/chat/... -run 'TestOpencode|TestPublishOpenCodeGateVerdict' -count=1
```

Penolakan tersanitasi dan `Retry-After` untuk klien ada di [API.md](API.md);
alur pemilihan account/pool ada di [ARCHITECTURE.md](ARCHITECTURE.md).
