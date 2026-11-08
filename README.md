# Google Photos → Immich album sync

`immich-sync` recreates Google Photos album membership in an existing Immich
library. It reads Google Takeout metadata, finds the matching assets already in
Immich, and adds those assets to albums.

It also migrates text entries from Google Photos `sharedAlbumComments`. Photo
comments are added to the matching Immich asset and album comments to the
matching Immich album. Immich attributes API-created comments to the API-key
owner, so the original Google author is preserved in the text as
`Name: comment [migrated from Google Photos]`. Likes without text are ignored.

> **No image upload, import, or deletion.** Existing Immich album members are
> preserved. For a complete Google Photos import, use a tool such as
> [immich-go](https://github.com/simulot/immich-go).

This repository is a maintained fork of
[web-gears/immich-sync](https://github.com/web-gears/immich-sync), with safer
matching, deterministic processing, bounded API retries, metadata-only
Takeout support, album descriptions, and a more informative CLI.

## Quick start

Requirements: Go 1.27.3+, an Immich API key that can search assets and modify
albums, and an extracted Google Photos Takeout. Configure the API and Takeout
directory, then run from the repository root:

```shell
export IMMICH_API_KEY="your-immich-api-key"
export IMMICH_API_URL="https://immich.example/api/"
export IMMICH_TAKEOUT_PATH="/path/to/Takeout 2026023/Google Photos"

make run
```

`IMMICH_API_URL` must be an HTTP(S) URL ending in `/api` (with or without a
trailing slash). `IMMICH_TAKEOUT_PATH` must point to the **Google Photos**
directory itself—not the outer `Takeout ...` directory. API keys are never
written to disk by the program. Missing required values are requested
interactively.

During album synchronization, the program asks for the minimum number of
matched Immich images required before creating a new album. The default is 3,
and values from 0 to 100 are accepted. This threshold only applies when some
Takeout images do not match; an album whose entire Takeout contents match is
created regardless of the threshold. Setting the threshold to 0 also allows
empty or completely unmatched albums to be created.

At startup, the program also asks whether archived Google Photos items should
be included. Choosing **No** removes Takeout entries with `"archived": true`
before album counts, matching, and the minimum-match threshold are evaluated.
This makes archived items behave as though they were never part of the album.

Build and run the binary instead:

```shell
make build
./bin/immich-sync
./bin/immich-sync -y        # skip album confirmations
```

## CLI preview

The following is a representative run with generic paths, names, and counts.

```text
➜  immich-sync git:(main) IMMICH_TAKEOUT_PATH="/Users/example/immich-sync/takeout-json/Takeout 2026023/Google Photos" make run
go run .

📸 Google Photos → Immich sync
────────────────────────────────────────────────────────
? Are you Alex Example? (y/N) y
? Do you want to sync your albums? (Y/n) y
? Include archived Google Photos items? (Y/n) n
? Minimum matched images for new albums (0-100) (default: 3):

📦 Google Takeout
────────────────────────────────────────────────────────
ℹ Reading album metadata...
  ▸ Family brunch 2025                         6 images
  ▸ Summer trip 2025                          215 images
  ▸ Wedding reception 2024                    590 images
  ▸ ...
✓ Found 12 albums with 1,352 images

🗂️ Immich albums
────────────────────────────────────────────────────────
ℹ Loading albums...
✓ Found 9 albums

🖼️ Album: Summer trip 2025
────────────────────────────────────────────────────────
? Sync 215 Takeout images into this album? (Y/n) y
✓ Updated album: added 215 assets
ℹ Comments for Summer trip 2025: found 3, added 3, skipped 0

✓ Album synchronization complete

📊 Sync summary
────────────────────────────────────────────────────────
  [+]  Albums created       │      1
  [~]  Albums updated       │      4
  [=]  Albums unchanged     │      7
  [!]  Albums skipped       │      0
  [+]  New assets added     │    215
  [?]  Comments found       │      3
  [+]  Comments added       │      3
```

When output is redirected, or `NO_COLOR=1` / `TERM=dumb` is set, colors and
animated progress are disabled. Use `A` at an album prompt to approve the
current and all remaining album actions.

## What it does

### Album synchronization

For each Takeout album, the tool:

1. reads the album title and metadata;
2. searches Immich by exact original filename plus a timestamp window of one
   day before to one day after the Google timestamp;
3. loads existing album members using the paginated, album-scoped metadata
   search;
4. adds only missing Immich asset IDs, preserving all existing members.

Matching is based on filename and time—not image content. Duplicate filenames
near the same timestamp can therefore be ambiguous. Identical searches are
cached, up to six unique lookups run concurrently, and Takeout ordering remains
deterministic.

Albums are processed newest first when their `metadata.json` has a creation
timestamp, then lexicographically for ties or undated albums. A non-empty
Google album description is copied only when a new Immich album is created.
Archived items are included by default; the startup choice can exclude them
before matching and threshold checks.
Text comments are migrated as the current Immich API-key user because Immich
does not provide an anonymous or Google-user identity for this import. Existing
matching comments are detected within their album and asset scope before
creation, so rerunning the sync reports the comments as found and skipped but
adds zero duplicates. For every album with Takeout comments, the sync logs the
number found, added, and skipped; albums without comments produce no comment
line.
By default, empty albums and albums with fewer than three matching Immich
assets are skipped rather than created when some Takeout images are unmatched.
The threshold can be changed at startup; a threshold of 0 allows empty albums.

The CLI ends album synchronization with a summary of `created`, `updated`,
`unchanged`, and `skipped` albums plus the number of newly added assets. It
also handles HTTP 429 responses centrally: it honors `Retry-After` and
`X-RateLimit-Reset`, adds 750–3000 ms of random jitter, coordinates cooldowns
across concurrent requests, and stops after five bounded retries. If no server
cooldown is supplied, it uses exponential backoff from one minute up to five
minutes. A reverse proxy such as Traefik may need its rate limit adjusted.

### Date synchronization

Choose **No** at the album prompt and **Yes** at the date prompt. The tool
recursively reads Takeout sidecars and compares their dates with Immich:

- enter `YYYY-MM-DD` to compare assets from one day;
- leave the date empty to compare all discovered files by filename.

Date changes to `dateTimeOriginal` always require confirmation, including when
`-y` is used.

## Takeout layout

Album synchronization expects album directories directly under `Google Photos`,
each with a `metadata.json`:

```text
Takeout 2026023/
└── Google Photos/
    ├── Family brunch 2025/
    │   ├── metadata.json
    │   └── IMG0001.jpg.supplemental-metadata.json
    └── Summer trip 2025/
        ├── metadata.json
        └── ...
```

Image files are not required. Recognized sidecars include:

- `photo.jpg.supplemental-metadata.json`
- `photo.jpg.suppl.json`
- `photo.jpg.suppl(1).json`

## Prompts and Make targets

The supported CLI option is `-y`:

```text
Y or Enter  sync the current album
n            skip the current album
A            sync/create the current album and approve the rest
-y           skip non-required confirmations

The archived-items prompt defaults to including archived items; answer `n` to
exclude entries whose Takeout metadata contains `"archived": true`.
The startup threshold prompt accepts a number from 0 to 100 and defaults to 3.
```

Useful targets:

```text
help         list targets
build        build bin/immich-sync
run          run the program; pass flags with ARGS="..."
test         run unit tests
vet          run go vet
fmt          format Go source
fmt-check    check Go formatting
check        run formatting, tests, vet, and build
clean        remove build artifacts
```

The Make targets use the current shell environment, so all three `IMMICH_*`
variables work with `make run` and the built binary.

## Metadata-only extraction

The image files are unnecessary when only the metadata is needed. Install
7-Zip and extract only the JSON files as follows.

On macOS with Homebrew:

```shell
brew install 7-zip
mkdir -p /path/to/takeout-json
7zz x "/path/to/Google_Takeout_Photos.7z" \
  -o"/path/to/takeout-json" -r "*.json"
export IMMICH_TAKEOUT_PATH="/path/to/takeout-json/Takeout 2026023/Google Photos"
```

On Linux with Arch Linux or Omarchy:

```shell
sudo pacman -S 7zip
mkdir -p /path/to/takeout-json
7z x "/path/to/Google_Takeout_Photos.7z" \
  -o"/path/to/takeout-json" -r "*.json"
export IMMICH_TAKEOUT_PATH="/path/to/takeout-json/Takeout 2026023/Google Photos"
```

The same commands work for `.zip`; use `7zz l archive.7z` on macOS or
`7z l archive.7z` on Linux to inspect an archive without extracting it. Solid
7z archives may still require reading a large part of the archive, but the
extracted result remains small.

## Troubleshooting

### `No albums found`

The path is probably too high or too low. It should usually end in:

```text
.../Takeout 2026023/Google Photos
```

### No assets match

Confirm that the asset already exists in Immich and that its original filename
matches the sidecar `title`. Also check the `/api/` URL suffix and API-key
permissions. External-library assets work only when returned by Immich's
metadata search; this tool does not upload missing images.

### HTTP 429 Too Many Requests

Retries are automatic and bounded. If failures continue, inspect rate limiting
in the Immich reverse proxy and consider reducing search concurrency in
`main.go`.

### API validation errors

The metadata search requires full ISO 8601 timestamps. The program generates
them automatically, so errors usually indicate an incorrect API path or an
incompatible Immich version.

## Development

Run checks from the repository root:

```shell
make check
```

Tests cover Takeout sidecar parsing and filesystem discovery; they do not
contact a live Immich server. Do not run live album mutations, date updates, or
destructive operations as part of validation.
