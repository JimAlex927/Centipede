# Docmost frontend

This is the React frontend colocated with the Go backend. It is based on the
`codex/frontend-websocket-before-perf` branch and uses the native WebSocket
realtime client.

## Local development

Start the Go backend on port `7788`, then run these commands from
`Centipede/frontend`:

```powershell
pnpm install
pnpm dev
```

The browser application is served at `http://localhost:5173`. In development,
`/api`, `/collab`, and `/realtime` are proxied to `http://localhost:7788`.
Set `APP_URL` in the Go repository `.env` when the backend uses another URL.

## Production build

Build the static frontend from this directory:

```powershell
pnpm build
pnpm preview
```

The generated files are in `dist/`. A production reverse proxy must forward
`/api`, `/collab`, and `/realtime` to the Go backend, including WebSocket
upgrade requests for the latter two paths.
