# Passwordless Cloud SQL — Go + IAM Database Authentication

> Connect a Go web service to **Google Cloud SQL for PostgreSQL** with **zero static passwords**, using short‑lived **IAM OAuth 2.0 tokens** over enforced **TLS**.

<p>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="Cloud SQL" src="https://img.shields.io/badge/Google%20Cloud%20SQL-PostgreSQL-4285F4?logo=googlecloud&logoColor=white">
  <img alt="Auth" src="https://img.shields.io/badge/Auth-IAM%20(passwordless)-2ea44f">
  <img alt="TLS" src="https://img.shields.io/badge/Transport-TLS%20required-663399">
  <img alt="License" src="https://img.shields.io/badge/License-MIT-lightgrey">
</p>

---

## Why this project

Static database passwords are one of the most common causes of breaches: they get committed to Git, pasted into Slack, copied into CI logs, and rarely rotated. This project demonstrates a **credential‑less** alternative that many teams still under‑use:

- **No password anywhere** — not in code, not in `.env`, not in a secret manager.
- The app authenticates as an **IAM principal** (`alice@example.com`) and mints a **short‑lived (~1 hour) OAuth 2.0 access token** on demand, using it as the database password *just in time*.
- Tokens **auto‑refresh** for long‑running services and **auto‑expire**, so a leaked token is useless within the hour.
- All traffic is forced over **TLS** (`sslmode=require`).

The result: authentication that is centrally governed by Google IAM, fully auditable in Cloud Audit Logs, and revocable in one click — no password reset, no redeploy.

---

## How it works

```
                                  ┌──────────────────────────────────────┐
                                  │            Go service (Echo)          │
                                  │                                       │
  gcloud auth ADC ──credentials──▶│  google.FindDefaultCredentials(...)   │
                                  │            │  scope:                   │
                                  │            │  sqlservice.login         │
                                  │            ▼                           │
                                  │       TokenSource  ──(cache/refresh)   │
                                  │            │                           │
                                  │   pgxpool.BeforeConnect hook           │
                                  │            │  cfg.Password = token     │
                                  └────────────┼──────────────────────────┘
                                               │  TLS (sslmode=require)
                                               ▼
                                  ┌──────────────────────────────────────┐
                                  │   Cloud SQL for PostgreSQL            │
                                  │   cloudsql.iam_authentication = on    │
                                  │   validates token ↔ IAM principal     │
                                  └──────────────────────────────────────┘
```

The core trick lives in [`db/db.go`](db/db.go):

1. Load **Application Default Credentials (ADC)** with the `https://www.googleapis.com/auth/sqlservice.login` scope.
2. Build a `pgxpool` config **without a password**.
3. Register a `BeforeConnect` hook. `pgx` calls it before opening *every* new connection, so it injects a **fresh** token as the password each time — handling expiry transparently for the whole lifetime of the pool.

```go
config.BeforeConnect = func(ctx context.Context, cfg *pgx.ConnConfig) error {
    tok, err := tokenSource.Token() // cached + auto-refreshed
    if err != nil {
        return fmt.Errorf("failed to fetch IAM OAuth2 token: %w", err)
    }
    cfg.Password = tok.AccessToken // short-lived token as the password
    return nil
}
```

---

## Security highlights

| Concern | Approach in this project |
| --- | --- |
| **No static secrets** | Password is a runtime OAuth token; nothing sensitive is stored or committed. |
| **Least-privilege identity** | The IAM user only holds `roles/cloudsql.instanceUser` + a mapped DB user. |
| **Short-lived credentials** | Tokens live ~1h and auto-refresh; blast radius of a leak is minimal. |
| **Encryption in transit** | `sslmode=require` — the server rejects plaintext connections. |
| **No error/info leakage** | DB errors are logged server-side; clients get a generic message (see `handleDBInfo`). |
| **Secrets kept out of Git** | `.env` and build artifacts are git-ignored; only `.env.example` with placeholders ships. |
| **Auditability** | Every connection is attributable to an IAM principal in Cloud Audit Logs. |

> **Note on placeholders:** all hosts, project IDs, and identities in this repo are fictional
> (`alice@example.com`, `10.0.0.5`, `my-project:...`). Swap in your own before running.

---

## Prerequisites (Google Cloud setup)

### 1. Enable IAM database authentication
On your Cloud SQL instance, set the flag `cloudsql.iam_authentication = on` and require SSL.

### 2. Create a database user mapped to your IAM identity
```bash
gcloud sql users create "alice@example.com" \
    --instance=<INSTANCE_ID> \
    --type=IAM_USER
```

### 3. Grant the least-privilege IAM role
```bash
gcloud projects add-iam-policy-binding <PROJECT_ID> \
    --member="user:alice@example.com" \
    --role="roles/cloudsql.instanceUser"
```

### 4. Authenticate locally with ADC
```bash
gcloud auth application-default login
```

---

## Configuration

```bash
cp .env.example .env
```

| Variable | Example | Notes |
| --- | --- | --- |
| `DB_HOST` | `10.0.0.5` | Cloud SQL private IP, or `127.0.0.1` when using the Auth Proxy |
| `DB_PORT` | `5432` | |
| `DB_USER` | `alice@example.com` | IAM user — **no password** |
| `DB_NAME` | `postgres` | |
| `DB_SSLMODE` | `require` | `disable` \| `require` \| `verify-ca` \| `verify-full` |
| `PORT` | `8080` | HTTP port for the Echo server |

`.env` is git-ignored, so your real values never leave your machine.

---

## Run

```bash
go mod tidy
go run .
```

You should see the pool ping succeed:
```
Pinging database to verify IAM credentials...
Database connection pool successfully verified and initialized!
```

---

## Endpoints

**Health check**
```bash
curl http://localhost:8080/health
# {"status":"UP","time":"..."}
```

**Database identity check** — proves the IAM auth + TLS path end to end:
```bash
curl http://localhost:8080/db-info
```
```json
{
  "current_user": "alice@example.com",
  "database": "postgres",
  "version": "PostgreSQL ... on x86_64-pc-linux-gnu ...",
  "server_time": "2026-06-20T15:00:00Z"
}
```
`current_user` echoing your IAM email is the proof: PostgreSQL authenticated you by identity, with no password ever supplied.

---

## Connecting with a GUI (DBeaver, etc.) via the Auth Proxy

GUI clients rarely speak Cloud SQL IAM natively. Tunnel through the **Cloud SQL Auth Proxy**, which handles IAM auth for you using your local ADC:

```bash
# macOS
brew install cloud-sql-proxy

# --private-ip for private networking, --auto-iam-authn to authenticate via ADC
cloud-sql-proxy --auto-iam-authn --private-ip --port 5433 <INSTANCE_CONNECTION_NAME>
# e.g. my-project:asia-southeast2:my-instance
```

Then create a PostgreSQL connection:
- **Host:** `127.0.0.1`  **Port:** `5433`  **Database:** `postgres`
- **Username:** `alice@example.com`  **Password:** *(leave empty — the proxy handles auth)*
- In **Driver properties**, set `sslmode` to `prefer` so the client doesn't reject the proxy's local listener.

---

## Project structure

```
.
├── main.go          # Echo server, routes, minimal .env loader
├── db/
│   └── db.go        # pgxpool + IAM token injection (BeforeConnect)
├── .env.example     # Config template (safe to commit)
├── go.mod / go.sum
└── README.md
```

---

## Tech stack

- **Go** with [Echo](https://echo.labstack.com/) for HTTP
- [`jackc/pgx/v5`](https://github.com/jackc/pgx) connection pooling
- [`golang.org/x/oauth2/google`](https://pkg.go.dev/golang.org/x/oauth2/google) for ADC / token sourcing
- **Google Cloud SQL for PostgreSQL** with IAM database authentication

---

## License

MIT — see [`LICENSE`](LICENSE).
