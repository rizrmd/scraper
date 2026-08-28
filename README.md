# Brand24 Scraper API

Backend Go untuk mengakses Brand24 secara server-side menggunakan sesi login panel. Service menemukan project akun secara otomatis dan menyediakan sinkronisasi mentions lengkap dengan pemecahan rentang 31 hari, pagination (500 item/page), retry eksponensial, timeout, dan error propagation. Official Data API tetap didukung sebagai jalur opsional bila key tersedia.

## Konfigurasi

Salin `.env.example` dan isi `BRAND24_EMAIL`, `BRAND24_PASSWORD`, serta `API_TOKEN` di runtime. Jangan commit secret. `API_TOKEN` melindungi seluruh endpoint data dengan `Authorization: Bearer <token>`. `BRAND24_API_KEY` dan `BRAND24_ACCOUNT_ID` hanya opsional untuk akses gateway official Data API.

## Endpoint

- `GET /healthz` — liveness tanpa akses upstream.
- `GET /readyz` — memastikan kredensial login atau Data API key tersedia.
- `GET /v1/brand24/session` — verifikasi login Brand24.
- `POST /v1/brand24/sync/mentions` — ambil seluruh mention untuk rentang tanggal.
- `/v1/brand24/data/*` — transparent server-side gateway ke `/api-data/v1/*`; mendukung GET/POST dan seluruh endpoint Brand24 saat ini maupun yang ditambahkan kemudian.

Contoh sinkronisasi:

```bash
curl -sS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"date_from":"2026-01-01","date_to":"2026-08-28","source":"tiktok"}' \
  http://localhost:3000/v1/brand24/sync/mentions
```

`project_id` boleh dikirim untuk memilih project tertentu. Jika tidak dikirim, scraper memilih project aktif pertama dari akun Brand24 secara otomatis.

Run locally: `go test ./... && go run ./cmd/server`.
