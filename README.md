# eclaim-mcp

Layanan Go independen untuk mengambil status klaim kendaraan dari `eclaim-api`.
Tool MCP `get_claim_status` menerima nomor registrasi dan empat digit terakhir
nomor HP terdaftar. Layanan memverifikasi input melalui endpoint tracking v2
sebelum mengambil status dan waktu aktivitas terkini.

## Konfigurasi

| Variabel | Nilai |
| --- | --- |
| `ECLAIM_API_BASE_URL` | URL dasar internal `eclaim-api` |
| `ECLAIM_API_CLIENT_ID` | Nomor client yang digunakan oleh endpoint tracking |
| `MCP_AUTH_TOKEN` | Token antarservice minimal 32 karakter tanpa spasi |
| `ECLAIM_MCP_LISTEN_ADDR` | Default `127.0.0.1:8080` |
| `ECLAIM_API_TIMEOUT` | Default `10s` untuk verifikasi dan tracking secara keseluruhan |

## Menjalankan dengan Docker

Compose membangun image dengan Go `1.27.4`. Dari folder project, salin
`.env.example` ke `.env` dan isi URL internal `eclaim-api`, ID client, serta
token yang sama dengan bot.

```powershell
Copy-Item .env.example .env
# Isi .env terlebih dahulu.
docker compose up -d --build
docker compose ps
curl.exe --fail http://127.0.0.1:8080/health
```

Port bawaan Docker dipublikasikan hanya di loopback. Untuk bot yang berjalan di
host yang sama, set `MCP_TOOL_URL=http://127.0.0.1:8080/mcp`. Jika port host
8080 sudah dipakai, set `ECLAIM_MCP_HOST_PORT` di `.env` dan gunakan port
tersebut pada URL bot dan pemeriksaan health.

Jika `eclaim-api` berjalan langsung di host Windows dengan Docker Desktop,
gunakan alamat seperti `http://host.docker.internal:1999` selama port API
dapat dijangkau dari container. Jika `eclaim-api` dan bot juga berjalan di
Docker, hubungkan layanan ke jaringan Docker yang sama. Contoh override berikut
memakai jaringan Compose `eclaim-api` yang sudah ada. Sesuaikan nama jaringan
dan port dengan deployment Anda.

```yaml
# compose.override.yaml di eclaim-mcp
services:
  eclaim-mcp:
    networks:
      - claims

networks:
  claims:
    external: true
    name: eclaim-api_mynet
```

Dalam konfigurasi jaringan bersama, gunakan
`ECLAIM_API_BASE_URL=http://eclaim-api:1999` di MCP dan
`MCP_TOOL_URL=http://eclaim-mcp:8080/mcp` di bot. Tambahkan jaringan eksternal
yang sama pada layanan bot jika Compose-nya terpisah.

## Menjalankan langsung dengan Go

Gunakan Go `1.27.4`. Set variabel di lingkungan proses, lalu jalankan `go run .` atau build dengan
`go build -o eclaim-mcp .`. Pastikan `GET /health` mengembalikan HTTP 200.
Jalankan satu replika pada jaringan internal. Gunakan HTTPS bila layanan
berkomunikasi lintas host. Beri bot `MCP_TOOL_URL` dengan akhiran `/mcp` dan
`MCP_AUTH_TOKEN` yang sama melalui secret deployment.

Batas percobaan adalah lima verifikasi per nomor registrasi dalam 15 menit.
Hitungan disimpan di memori proses dan kembali kosong saat proses dimulai ulang.
Untuk data riil, gunakan `eclaim-api` dalam mode produksi agar request dan
response body tracking tidak masuk ke log upstream.

## Menguji tool dengan MCP Inspector

Jalankan `eclaim-mcp` terlebih dahulu. Lalu buka UI Inspector dari PowerShell:

```powershell
npx.cmd @modelcontextprotocol/inspector
```

1. Buat koneksi **Streamable HTTP** ke `http://127.0.0.1:8080/mcp`. Sesuaikan
   port jika `ECLAIM_MCP_HOST_PORT` berbeda.
2. Tambahkan header `Authorization` dengan nilai `Bearer <MCP_AUTH_TOKEN>`,
   lalu klik **Connect**.
3. Buka **Tools**, tampilkan daftar tool, lalu pilih `get_claim_status`.
   Isi `registration_number` dan `phone_last4` dari data uji. Masukkan empat
   digit sebagai string agar nol di depan tetap terbaca.
4. Untuk data yang cocok, periksa `status` dan, bila tersedia,
   `activity_label` serta `updated_at`. Ulangi dengan empat digit yang salah.
   Respons yang diharapkan adalah `VERIFICATION_FAILED` tanpa status klaim.

Inspector menguji koneksi dan hasil tool secara langsung. Uji percakapan
lintas pesan tersedia di `ai-asuransiku` melalui `TestClaimStatusConversation`.
`go test ./...` di project ini menguji MCP dengan `eclaim-api` tiruan.
