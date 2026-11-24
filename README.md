# voidgrid-shopping

A self-hosted recipe box, meal planner and shopping list, as a responsive website (no app to install). It started as a
replacement for Plan to Eat, with what Plan to Eat lacks: nutrition worked out per ingredient, and prices.

Go, server-rendered HTML, SQLite, one Docker container. Dark theme only.

> **Beta 1.** It is built and tested, but it has had one household's use, not many. Back up your data (see
> [Backups](#backups)) and read [Known limits](#known-limits-in-this-beta).

## Features

- **Recipes** – manual entry (ingredient rows with amount, unit, ingredient, notes and headings, or bulk text), photos,
  tags, rating, search and filters (course, cuisine, main ingredient, time, ingredients with any / all / without), random
  recipe, print view. Recipes show **exactly as written**; scaling only multiplies, and a US or metric view is
  something you
  choose, never automatic.
- **Importers** – Plan to Eat CSV export (with photo download), website address (schema.org data), pasted text, TikTok
  caption (through yt-dlp, caption only), and JSON-LD or saved-page files. Every importer opens a draft to
  review; nothing is
  saved until you press Save.
- **Planner** – week view with breakfast, lunch, snack and dinner, drag and drop, leftovers, prep notes.
- **Shopping list** – built from the planned meals. Ingredients merge only when they are exactly the same item (same name,
  same pack, same kind of unit); nothing is fuzzy-matched. Stores and aisles are learned as you assign items.
  Every item is
  editable. Staples are a separate list you copy onto the list when you want them (and can import from Plan to Eat).
- **Prices** – record what you paid per package or per weight, with brand; the last price is remembered per item and store;
  a running total counts what you have bought.
- **Nutrition** – calories, protein, carbs and fat per serving, calculated from foods *you* choose for each ingredient
  (USDA FoodData Central, a product scanned or typed by barcode from Open Food Facts, or typed by hand), or
  entered for the
  whole recipe. Never guessed.
- **Several people, one household** – admin invites and password-reset links (emailed, or copied if email is not set up),
  optional authenticator-app codes with recovery codes, optional passkeys, optional single sign-on (OpenID Connect, built
  against Pocket ID) with a required group.
- **Public share links** – share one recipe with anyone: every link expires (1 to 365 days), can be withdrawn at any time,
  shows only the recipe, and is not indexed by search engines.
- **Phone friendly** – bottom tab bar; the shopping list stays in step across devices and **works offline**
  (check-offs and
  prices are saved on the phone and sent when it reconnects).
- **Backups** – a consistent database snapshot is written on a schedule for your backup tool to pick up.

## Quick start (Docker Compose)

You need Docker with the Compose plugin, and a reverse proxy that serves the app over **HTTPS** (session cookies are always
`Secure`, and passkeys need HTTPS; there is no plain-HTTP mode).

```sh
git clone git@github.com:voidgrid/voidgrid-shopping.git && cd voidgrid-shopping
cp compose.example.yaml compose.yaml
openssl rand -base64 32 > secret_key.txt && chmod 600 secret_key.txt
docker compose up -d --build
docker compose logs shopping
```

The log shows a one-time link such as `/setup?token=…`. Open it (through your proxy) to create the first
account, which is the
admin. The link stops working once the account exists.

The compose file binds the app to `127.0.0.1:8800` and keeps everything (database, photos, backups) in one volume,
`shopping-data`, mounted at `/data`.

### Reverse proxy

Point your proxy at `127.0.0.1:8800`. With Caddy:

```caddyfile
shopping.example.com {
    reverse_proxy 127.0.0.1:8800
}
```

Set `VGS_TRUST_PROXY=true` (the compose file does) only when the proxy overwrites `X-Forwarded-For`, as Caddy, nginx and
Traefik do by default. It makes the sign-in and share-link rate limits use the visitor's address instead of the proxy's.

### First steps after signing in

1. **Settings → Public address**: enter `https://shopping.example.com`. Emailed links, share links and passkeys are built
   from it; without it they are switched off.
2. **Settings → Email (SMTP)** (optional): lets invites and resets be emailed. Without it you copy the link and send it
   yourself.
3. **Recipes → Import**: bring in your Plan to Eat recipes (`plantoeat-recipes-….csv`), or a web page, text or TikTok link.
4. **Staples**: import your Plan to Eat grocery-list export, or add staples by hand.
5. **Nutrition data** (optional): load USDA FoodData Central once, see below.

### Nutrition data

Download the **SR Legacy** CSV zip from <https://fdc.nal.usda.gov/download-datasets> (public domain; the USDA asks to be
credited), then:

```sh
docker cp FoodData_Central_sr_legacy_food_csv_2018-04.zip shopping:/tmp/usda.zip
docker exec shopping voidgrid-shopping admin import-usda /tmp/usda.zip
```

This also supplies weight-per-cup data, which the shopping list uses to merge, say, "1 cup flour" and "200 g
flour". For each
ingredient you pick the matching food once (search by words); the choice is remembered. Nothing is matched automatically.

## Configuration

Environment variables (everything else is on the admin **Settings** tab):

| Variable | Default | Meaning |
| --- | --- | --- |
| `VGS_LISTEN_ADDR` | `:8800` (`0.0.0.0:8800` in the image) | listen address |
| `VGS_DB_PATH` | `/data/voidgrid-shopping.db` | SQLite file |
| `VGS_PHOTO_DIR` | `photos` beside the database | recipe photo files |
| `VGS_SECRET_KEY` / `VGS_SECRET_KEY_FILE` | unset | at least 16 characters, or a file holding them (a Docker secret). Encrypts the SMTP password, the single sign-on client secret and authenticator-app secrets. Without it those cannot be saved. **Keep it with your backups**: changing or losing it makes stored secrets unreadable |
| `VGS_BACKUP_DIR` | `backups` beside the database | where snapshots go |
| `VGS_BACKUP_HOURS` | `24` | hours between snapshots; `0` turns them off. One is also made at start when the newest is older than this |
| `VGS_BACKUP_KEEP` | `7` | how many snapshots to keep |
| `VGS_TRUST_PROXY` | `false` | take the visitor's address from the last `X-Forwarded-For` value; set only behind a proxy that overwrites it |
| `VGS_YTDLP_PATH` | `yt-dlp` | program used to read TikTok captions (installed in the image) |

## Accounts and sign-in

- The first account is the admin. After that, **Users** (admin) invites people: they get a link to choose their own password.
  A reset link works the same way; an admin never sees or sets anyone's password.
- **Account** (everyone): change password; optional authenticator-app codes (with 10 one-time recovery codes); optional
  passkeys. Nothing requires a passkey.
- **Single sign-on**: Settings → Single sign-on. Enter the provider address, client ID and secret. In the
  provider, set the
  redirect address shown on that card (`https://…/login/oidc/callback`). The *required group* decides who may sign in (and
  have an account created on first sign-in); an optional *admin group* makes members admins. An existing account is linked
  only through a verified email address.
- Locked out? Console commands (run inside the container):

```sh
docker exec shopping voidgrid-shopping admin reset-login <email>        # new random password, shown once
docker exec shopping voidgrid-shopping admin disable-passkeys <email>   # remove their passkeys
docker exec shopping voidgrid-shopping admin disable-totp <email>       # remove authenticator codes
```

## Backups

The app writes a checked, consistent copy of the database (`voidgrid-shopping-YYYYMMDD-HHMMSS.db`, mode 600) into the
backups folder on the volume, every 24 hours by default, keeping the newest 7. Make one now with:

```sh
docker exec shopping voidgrid-shopping admin backup
```

Back up the **whole `/data` volume** with your backup tool (snapshots and the `photos` folder), and keep `secret_key.txt`
with it. Photos are plain files and are not inside the snapshot.

To restore: stop the container; delete `voidgrid-shopping.db` and any `-wal` / `-shm` files from the volume; copy a
snapshot in as `voidgrid-shopping.db`; start the container. Snapshots contain password hashes and encrypted
secrets, so keep
them private. Encrypted secrets open only with the same secret key.

## Updating

```sh
git pull && docker compose up -d --build
```

The database migrates itself at start. Take a snapshot first (`admin backup`).

## Security notes

- Meant to be exposed to the internet behind your HTTPS proxy: Secure, HttpOnly, SameSite cookies; cross-site POSTs
  refused; strict content-security policy; HSTS; rate-limited sign-in, two-step codes and share links; secrets
  encrypted at
  rest; passwords hashed with Argon2id; recipe photos served only to signed-in people (or through a live share link).
- Imports from a web address fetch public sites only; addresses that resolve to private, loopback or cloud-metadata
  ranges are refused.
- Public share links are read-only pages on the same address as the app. They run no user script and ignore sign-in
  cookies, but they are not on a separate domain.
- Only a hash of each invite, reset and share token is stored.

## Known limits in this beta

- **Barcode scanning** uses the browser's built-in detector (Chrome and Chromium, including Chrome on Android). It has not
  been tried on a real phone camera, and there is no fallback for other browsers; typing the number always works.
- **Passkeys** are tested with a software authenticator, not yet with real devices. **Single sign-on** is
  tested against a
  mock provider, not yet against a live Pocket ID (check the group claim name).
- **TikTok caption import** depends on yt-dlp, which TikTok changes often; the version in the image is the Alpine
  package's and may go stale. A caption is loose text, so expect to tidy the draft.
- Offline mode covers the shopping list only (check-offs, prices). Last change wins when two phones disagree.
- Not built: cook mode, copying or moving a planner day, a recipe queue, JSON export with photos, scaling that
  pluralises ingredient names ("2 egg").
- The rate limits and pending sign-ins live in memory and reset when the container restarts.

## Third-party data and software

- **USDA FoodData Central** (public domain; please credit the USDA) for generic food nutrition and weight per volume.
- **Open Food Facts** (ODbL) for scanned products. Only the barcode is sent, with a generic User-Agent; results are
  cached.
- **yt-dlp** (Unlicense) is installed in the image to read TikTok captions.
- Go libraries: `modernc.org/sqlite`, `golang.org/x/crypto`, `go-webauthn/webauthn`, `coreos/go-oidc`, `golang.org/x/oauth2`,
  `skip2/go-qrcode` (see `go.mod`).

## Development

Docker-first, no host toolchain: `scripts/{fmt,vet,test,lint,build}.sh` (Go 1.27, golangci-lint v2.15.0, both in Docker).
Install the hooks with `pre-commit install`; they run gitleaks and the linter. The Docker build runs vet and
the tests, so an
image only exists if they pass. CI runs the linter and gitleaks on every push.

## License

MIT, © 2026 voidgrid. See `LICENSE`.
