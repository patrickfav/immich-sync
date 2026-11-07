# Google Photos Album Sync for Immich (without image copy)

## Fork and improvements

This repository is a maintained fork of
[web-gears/immich-sync](https://github.com/web-gears/immich-sync). It keeps the
original purpose—recreating Google Photos album membership in Immich when the
assets are already present—while making the sync safer, more predictable, and
easier to operate.

Compared with the original implementation, this fork adds or improves:

- deterministic album ordering, with newest albums first when dated;
- album merging by Immich asset ID without deleting existing members;
- filename-and-timestamp matching with pagination, caching, and bounded
  concurrency;
- pre-creation matching so empty or completely unmatched albums are never
  created;
- Google album descriptions copied to newly created Immich albums;
- metadata-only Google Takeout support, including sidecar filename variants;
- API URL and Takeout structure validation with clear startup errors;
- bounded HTTP 429 retries honoring server cooldown headers plus randomized
  750ms–3000ms jitter;
- clearer terminal output, the `A` approve-all option, and explicit album
  created/updated/unchanged/skipped statuses;
- focused unit tests, a Makefile, and expanded project documentation.

## Description

`immich-sync` copies album membership from a local Google Photos Takeout into
an existing Immich library. It is intended for cases where the same photos are
already in Immich, but the album organization only exists in Google Photos.

This tool does **not** import photos, upload image files, or create missing
library assets. It reads source metadata and uses the Immich API to add
already-existing assets to albums. For a complete Google Photos import, use a
dedicated importer such as [immich-go](https://github.com/simulot/immich-go).

## How it works

The program has two workflows:

1. **Album synchronization** loads album names and photo metadata from the
   Takeout directory. Each album directory must contain a Google
   `metadata.json`; its `title` becomes the Immich album name.
2. **Date synchronization** compares Takeout metadata with Immich assets and
   can optionally update incorrect dates.

For album synchronization, assets are searched in Immich by:

- exact `originalFileName` match; and
- a timestamp window of one day before to one day after the Google metadata
  timestamps.

The image contents are never compared. Duplicate filenames can therefore
produce an ambiguous or incorrect match, especially when different photos
were taken close together. When an Immich album with the same name already
exists, the program reads its current asset IDs through the album-scoped
metadata search, skips those IDs, and appends only missing assets. Existing
album members are preserved and nothing is removed.

Album matching caches identical filename/time-window searches and performs up
to six unique Immich lookups concurrently. The result order is still based on
the deterministic Takeout file order.

Immich API requests handle HTTP 429 responses with bounded retries. The program
uses the server's `Retry-After` header plus random 750ms-3000ms jitter when
present, then
`X-RateLimit-Reset`, and otherwise waits for one minute with exponential
backoff up to five minutes. It retries at most five times and coordinates the
cooldown across concurrent searches. Immich does not publish one universal
429 cooldown: the limit may also be imposed by a reverse proxy such as
Traefik, so a proxy-specific limit may need to be adjusted if retries keep
failing.

When creating a new Immich album, the non-empty Google album description is
copied to it. Albums with no discovered photo metadata are reported with a
warning and are not created. Albums whose Takeout files have no matching
Immich assets are also skipped before album creation.

## Requirements

- Go 1.27.3 or newer
- An Immich API key with permission to search assets and modify albums
- An Immich API URL including the `/api/` path, for example
  `https://immich.example/api/`; a missing trailing slash is normalized
- A Google Photos directory containing the extracted Takeout metadata

The program is run from a checkout or built with Go. The repository includes a
Makefile for the common development commands; it does not install a global
binary.

## Google Takeout layout

For album synchronization, set `IMMICH_TAKEOUT_PATH` to the **Google Photos**
directory itself, not its parent Takeout directory:

```text
Takeout 2026023/
└── Google Photos/
    ├── My Supercool album in 2025/
    │   ├── metadata.json
    │   ├── SNY00000.jpg.supplemental-metadata.json
    │   └── ...
    └── Another album/
        ├── metadata.json
        └── ...
```

The image files are not required for this tool. The relevant metadata files
are JSON sidecars such as:

- `photo.jpg.supplemental-metadata.json`
- `photo.jpg.suppl.json`
- `photo.jpg.suppl(1).json`

## Configuration

The program always reads these environment variables at startup:

```shell
export IMMICH_API_KEY="YourImmichApiKey"
export IMMICH_API_URL="https://immich.example/api/"
```

Also set:

```shell
export IMMICH_TAKEOUT_PATH="/path/to/Takeout 2026023/Google Photos"
```

If a required variable is missing, the program prompts for it. The API URL must
be a valid HTTP(S) URL ending in `/api` (with or without a trailing slash). The
Takeout value must point to an existing Google Photos directory. API keys are
never written to disk by the program.

## Build and usage

Clone the repository and enter its root directory:

```shell
git clone https://github.com/patrickfav/immich-sync.git
cd immich-sync
```

Build the binary into `bin/immich-sync`:

```shell
make build
```

Run the built binary:

```shell
./bin/immich-sync
```

Or run directly from the checkout:

```shell
make run
```

The only command-line option currently supported is `-y`:

```shell
./bin/immich-sync -y
```

The equivalent Make target is:

```shell
make run ARGS="-y"
```

### Make targets

Run `make help` to list the available targets:

```text
help         Show available targets
build        Build the binary into bin/
run          Run the program; pass CLI flags with ARGS="..."
test         Run unit tests
vet          Run go vet
fmt          Format Go source files
fmt-check    Check Go source formatting without changing files
check        Run formatting, tests, vet, and build
clean        Remove build artifacts
```

The Make targets use the current shell environment, so `IMMICH_API_KEY`,
`IMMICH_API_URL`, and `IMMICH_TAKEOUT_PATH` work with both `make run` and the
built binary.

The program collects and validates its configuration values, confirms the
Immich API user, and asks whether to sync albums. Album
synchronization asks for confirmation for each existing or new
album:

```text
Are you sure you want to sync this album? (Y/n/A)
```

- `Y` or Enter syncs the current album.
- `n` skips the current album.
- `A` syncs or creates the current album and approves all remaining album
  actions.
- `-y` skips non-required confirmations, including album and new-album
  confirmation prompts.

If an album title from Google Photos does not exist in Immich, the program
offers to create it with the non-empty Google Photos description and then adds
the matching assets. The matching search happens before the create request, so
an album is not created when zero Immich assets match. Albums with no discovered
image metadata are skipped and are not created.

Albums are processed newest first when their Takeout `metadata.json` includes a
creation timestamp. Albums without timestamps are processed afterward in
lexicographic order. If no album timestamps are available, all albums use
lexicographic order.

The duplicate check is based on Immich asset IDs, not filenames. This means a
photo is skipped only when that exact Immich asset is already in the matching
album. The album lookup is paginated, so albums larger than the API's default
search page are also handled.

The CLI uses light colors, separators, and status symbols when connected to a
terminal. Album discovery shows the Takeout image count, confirmations include
the number of images involved, and asset matching uses a compact progress bar.
Album results explicitly report whether an album was created, updated,
unchanged, or skipped. Colors and animated progress are automatically disabled
when output is redirected. Set `NO_COLOR=1` or `TERM=dumb` to disable them
explicitly.

## Date synchronization

Choose **No** at the album prompt and **Yes** at the date synchronization
prompt. The program recursively reads metadata sidecars and then asks for a
date:

- Enter `YYYY-MM-DD` to compare the Immich assets from that day with Takeout
  metadata.
- Leave the date empty to compare all discovered Takeout files by filename.

When a date differs, the program asks before updating Immich's
`dateTimeOriginal`. These update prompts are required and are not skipped by
`-y`.

## Extracting only metadata from a Takeout archive

The image files are unnecessary, so a large Takeout archive can be extracted
with only its JSON metadata. On macOS with Homebrew, install 7-Zip with:

```shell
brew install 7-zip
```

Homebrew provides the `7zz` executable. The `x` command preserves the archive's
directory structure:

```shell
mkdir -p /path/to/takeout-json
7zz x "/path/to/Google_Takeout_Photos.7z" \
  -o"/path/to/takeout-json" \
  -r "*.json"
```

The same command works for a `.zip` archive. After extraction, point the
program at the nested Google Photos directory, for example:

```shell
export IMMICH_TAKEOUT_PATH="/path/to/takeout-json/Takeout 2026023/Google Photos"
```

To inspect an archive without extracting it:

```shell
7zz l "/path/to/Google_Takeout_Photos.7z"
```

With a solid 7z archive, extracting only JSON still may require 7-Zip to read
large portions of the archive, but it keeps the extracted output small.

## Troubleshooting

### `No albums found`

The configured path is probably too high or too low in the Takeout tree. For
album synchronization it must contain the album directories directly, usually:

```text
.../Takeout 2026023/Google Photos
```

### No assets match

Check that the asset already exists in Immich and that its original filename is
the same as the filename in the Google sidecar's `title`. Also verify that
`IMMICH_API_URL` includes the `/api/` suffix and that the configured API key can
search the relevant assets. Assets in an external library are usable only if
they are returned by Immich's metadata search for that API key. This program
does not upload the missing image.

### HTTP 429 Too Many Requests

The program waits and retries automatically. If the response includes
`Retry-After` or `X-RateLimit-Reset`, that server-provided cooldown is used;
all cooldowns receive random 750ms-3000ms jitter.
Without either header, the fallback starts at one minute and backs off between
retries. If all retries are exhausted, check the Immich reverse proxy for a
rate-limit middleware or reduce the search concurrency in `main.go`.

### API validation errors

The Immich metadata search requires full ISO 8601 timestamps, not date-only
values. The current program generates those timestamps automatically; an API
URL pointing at the wrong path or an incompatible Immich version can still
produce validation errors.

## Development

Run the unit tests, static checks, and build from the repository root:

```shell
make check
make build
```

The tests focus on Takeout sidecar parsing and filesystem discovery. They do
not contact an Immich server.
