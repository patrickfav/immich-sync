# immich-sync

## Purpose

`immich-sync` synchronizes Google Photos album membership into an existing
Immich library. It reads Google Takeout metadata and uses the Immich API to
find existing assets and add them to albums. It does not upload image files,
import missing assets, or delete album members.

## Project structure

- `main.go` contains the CLI, configuration loading, Takeout discovery, and
  Immich API operations.
- `main_test.go` contains focused unit tests for local metadata parsing and
  filesystem discovery.
- `README.md` is the user-facing setup and operation guide.
- `go.mod` defines the module and requires Go 1.27.3.

## Configuration and secrets

- `config.json` is used when it exists in the current working directory; it
  takes precedence over environment variables.
- Otherwise use `IMMICH_API_KEY`, `IMMICH_API_URL`, and optionally
  `IMMICH_TAKEOUT_PATH`.
- `IMMICH_API_URL` must include the `/api/` suffix and a trailing slash.
- Never commit, print, or log API keys. Keep local configuration files
  private and use restrictive permissions such as `chmod 600 config.json`.
- Do not add real Takeout metadata, image files, logs, or extracted archives to
  the repository.

## Google Takeout behavior

- Album synchronization expects `IMMICH_TAKEOUT_PATH` to be the `Google Photos`
  directory containing album directories directly.
- Album discovery reads each album's `metadata.json` and reads JSON sidecars;
  image bytes are not required.
- Date synchronization recursively recognizes Google sidecars ending in
  `.supplemental-metadata.json`, `.suppl.json`, or `.suppl(1).json`.
- Asset matching uses the exact original filename plus a timestamp window. Do
  not replace this with content matching without documenting the behavior and
  adding tests for ambiguous filenames.
- Existing album membership is read with `POST /search/metadata` filtered by
  the album ID. Do not rely on `GET /albums/:id` returning an `assets` array;
  current Immich API responses omit that field. Add only asset IDs that are
  not already in the album and preserve all existing members.

## Development workflow

Use Go tooling from the repository root. Before handing off a change, run:

```shell
gofmt -w main.go main_test.go
go test ./...
go vet ./...
go build .
git diff --check
```

Add focused unit tests for new local behavior. Tests should use temporary
directories and test HTTP handlers or injected clients rather than contacting
a live Immich server. Do not add broad integration tests unless explicitly
requested.

When changing API requests:

- keep JSON payloads typed and valid;
- use full RFC 3339 timestamps for Immich datetime filters;
- preserve non-2xx response errors so configuration and API failures are
  visible to the user; and
- avoid logging credentials or response data that may contain sensitive
  information.

When changing prompts or CLI behavior, update `README.md` and add or update
tests where the behavior can be tested without a terminal or live server.
The current supported CLI option is `-y`; the album prompt also supports `A`
to approve the current and remaining album actions, including new album
creation.

Do not perform live album mutations, date updates, or destructive repository
operations as part of validation unless explicitly requested.
