# immich-sync

## Purpose

`immich-sync` synchronizes Google Photos album membership into an existing
Immich library. It uses local Google Takeout metadata and the Immich API to find
existing assets and add them to albums. It does not upload image files, import
missing assets, or delete album members.

## Project structure

- `main.go` contains the CLI, configuration, and sync orchestration.
- `immich_client.go` contains Immich API models, HTTP handling, pagination,
  matching queries, and rate-limit handling.
- `google_takeout.go` adapts Takeout data to the shared matching engine.
- `*_test.go` contains focused unit tests for local metadata parsing and
  filesystem discovery.
- `README.md` is the user-facing setup and operation guide.
- `go.mod` defines the module and requires Go 1.27.3.

## Configuration and secrets

- Always use `IMMICH_API_KEY` and `IMMICH_API_URL`.
- The program uses `IMMICH_TAKEOUT_PATH` and validates its structure.
- `IMMICH_API_URL` must end with `/api`; the program normalizes the trailing
  slash.
- Never commit, print, or log API keys.
- Do not add real Takeout metadata, image files, logs, or extracted archives to
  the repository.

## Google Takeout behavior

- Album synchronization expects `IMMICH_TAKEOUT_PATH` to be the `Google Photos`
  directory containing album directories directly.
- Startup validation checks that the path contains album subdirectories with
  `metadata.json` and clearly rejects the outer `Takeout ...` directory.
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
- Album asset searches may use the bounded worker pool and per-run query cache
  in `main.go`. Keep the concurrency limit conservative, preserve Takeout
  ordering when constructing asset IDs, and keep progress output in the main
  goroutine.
- Handle Immich HTTP 429 responses centrally. Honor `Retry-After` and
  `X-RateLimit-Reset` when provided, use bounded exponential fallback cooldowns
  otherwise, add 750ms-3000ms of random jitter, and coordinate the cooldown
  across concurrent requests. Do not turn a rate-limit response into an
  unbounded retry loop.
- Process albums deterministically: use descending Takeout album date
  timestamps when available, then lexicographic order for ties or undated
  albums. Never range over the album map directly for sync order.
- Copy a non-empty Google album description only when creating a new Immich
  album. Skip empty Takeout albums with a warning unless the threshold is 0;
  resolve matching Immich
  assets before creating a new album. By default, create a partially matched
  album only when at least three images match; ask for a startup threshold from
  0 through 100. Create a fully matched album regardless of the threshold, and
  allow a threshold of 0 to create empty or completely unmatched albums.
- At startup, ask whether archived Takeout items (`"archived": true`) should
  be included. If excluded, filter them before reporting album image counts,
  matching assets, and applying the minimum matched-image threshold; treat them
  as though they were never part of the album.

## Development workflow

Use the Makefile from the repository root for the common checks. After every
code change and before handing it off, always run `make check` to verify the
change. If `fmt-check` fails, run `make fmt` and then rerun `make check`:

```shell
make fmt && make check
```

The `check` target runs the formatting check, tests, vet, and build.
Editor diagnostics do not replace `make check`. If the local Go toolchain or
other required command is unavailable, report that exact verification blocker
and do not claim that the change passed the check.

The Makefile intentionally has no install target; run `make run` for local
execution or use the binary produced by `make build`.

Add focused unit tests for new local behavior. Tests should use temporary
directories and test HTTP handlers or injected clients rather than contacting
a live Immich server. Do not add broad integration tests unless explicitly
requested.

When changing API requests:

- keep JSON payloads typed and valid;
- use full RFC 3339 timestamps for Immich datetime filters;
- preserve non-2xx response errors so configuration and API failures are
-  visible to the user;
- keep API requests bounded by the shared HTTP client timeout; and
- handle HTTP 429 responses with the shared bounded cooldown/retry logic; and
- avoid logging credentials or response data that may contain sensitive
  information.

When changing prompts or CLI behavior, update `README.md` and add or update
tests where the behavior can be tested without a terminal or live server.
The current supported CLI option is `-y`; the album prompt also supports `A`
to approve the current and remaining album actions, including new album
creation. Album synchronization ends with a summary counting created, updated,
unchanged, and skipped albums plus successfully added assets.
- Keep CLI decoration restrained: use the standard library, short status
  symbols, and simple separators. Preserve readable plain output when stdout
  is redirected or `NO_COLOR`/`TERM=dumb` disables ANSI colors.

Do not perform live album mutations, date updates, or destructive repository
operations as part of validation unless explicitly requested.
