# Google Photos album sync for Immich

## Description

`immich-sync` copies album membership from a Google Photos Takeout into an
existing Immich library. It is intended for cases where the same photos are
already in Immich, but the album organization only exists in Google Photos.

This tool does **not** import photos, upload image files, or create missing
library assets. It reads Google Takeout metadata and uses the Immich API to add
already-existing assets to albums. For a complete Google Photos import, use a
dedicated importer such as [immich-go](https://github.com/simulot/immich-go).

## How it works

The program has two workflows:

1. **Album synchronization** scans the immediate subdirectories of the
   configured Takeout path. Each album directory must contain a Google
   `metadata.json`; its `title` becomes the Immich album name. The photo
   sidecar JSON files provide the original filename and timestamps.
2. **Date synchronization** recursively scans the configured path for Google
   Photos metadata sidecars. It can compare the Takeout timestamp with Immich
   assets and optionally update incorrect dates.

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

## Requirements

- Go 1.27.3 or newer
- An Immich API key with permission to search assets and modify albums
- An Immich API URL ending in `/api/`, for example
  `https://immich.example/api/`
- A Google Photos Takeout directory containing the extracted metadata

The program currently has no separate installer or package manager. It is run
from a checkout or built with Go.

## Google Takeout layout

For album synchronization, set `IMMICH_TAKEOUT_PATH` to the **Google Photos**
directory itself, not its parent Takeout directory:

```text
Takeout 2026023/
└── Google Photos/
    ├── Auf Kreta mit Theo #2025/
    │   ├── metadata.json
    │   ├── SNY00032_24MP.jpg.supplemental-metadata.json
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

Configuration is read from `config.json` in the current working directory if
that file exists. When it does not exist, the program uses environment
variables instead. `config.json` takes precedence over the environment.

### `config.json`

```json
{
  "apiKey": "YourImmichApiKey",
  "apiURL": "https://immich.example/api/",
  "takeoutPath": "/path/to/Takeout 2026023/Google Photos"
}
```

Keep this file private. On macOS and Linux, restrict it to your user:

```shell
chmod 600 config.json
```

### Environment variables

```shell
export IMMICH_API_KEY="YourImmichApiKey"
export IMMICH_API_URL="https://immich.example/api/"
export IMMICH_TAKEOUT_PATH="/path/to/Takeout 2026023/Google Photos"
```

`IMMICH_TAKEOUT_PATH` is optional when using environment variables; if omitted,
the current directory is used. Use an absolute path when possible.

## Installation and usage

Clone the repository and enter its root directory:

```shell
git clone https://github.com/patrickfav/immich-sync.git
cd immich-sync
```

Install the binary into Go's binary directory:

```shell
go install .
```

Ensure `$(go env GOPATH)/bin` is on your `PATH`, then run:

```shell
immich-sync
```

Alternatively, run directly from the checkout:

```shell
go run .
```

The only command-line option currently supported is `-y`:

```shell
immich-sync -y
```

The program first confirms the Immich API user, then asks whether to sync
albums. Album synchronization asks for confirmation for each existing or new
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
offers to create it and then adds the matching assets.

The duplicate check is based on Immich asset IDs, not filenames. This means a
photo is skipped only when that exact Immich asset is already in the matching
album. The album lookup is paginated, so albums larger than the API's default
search page are also handled.

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

### API validation errors

The Immich metadata search requires full ISO 8601 timestamps, not date-only
values. The current program generates those timestamps automatically; an API
URL pointing at the wrong path or an incompatible Immich version can still
produce validation errors.

## Development

Run the unit tests and static checks from the repository root:

```shell
go test ./...
go vet ./...
go build .
```

The tests focus on Takeout sidecar parsing and filesystem discovery. They do
not contact an Immich server.
