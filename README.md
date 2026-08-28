# Brand24 Scraper API

Backend Go untuk mengakses Brand24 secara server-side. Service memverifikasi login akun Brand24, meneruskan seluruh official Data API, dan menyediakan sinkronisasi mentions lengkap dengan pemecahan rentang 31 hari, cursor pagination (500 item/page), retry eksponensial, timeout, dan error propagation.

## Konfigurasi

Salin `.env.example` dan isi secret di runtime. Jangan commit secret. `BRAND24_API_KEY` dibuat oleh admin Brand24 pada halaman Account → API dan memerlukan Data API add-on. `API_TOKEN` melindungi seluruh endpoint data dengan `Authorization: Bearer <token>`.

## Endpoint

- `GET /healthz` — liveness tanpa akses upstream.
- `GET /readyz` — memastikan Data API key tersedia.
- `GET /v1/brand24/session` — verifikasi login Brand24.
- `POST /v1/brand24/sync/mentions` — ambil seluruh mention untuk rentang tanggal.
- `/v1/brand24/data/*` — transparent server-side gateway ke `/api-data/v1/*`; mendukung GET/POST dan seluruh endpoint Brand24 saat ini maupun yang ditambahkan kemudian.

Contoh sinkronisasi:

```bash
curl -sS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"project_id":"123456789","date_from":"2026-01-01","date_to":"2026-08-28"}' \
  http://localhost:3000/v1/brand24/sync/mentions
```

Run locally: `go test ./... && go run ./cmd/server`.
