# meetingroom

An in-memory meeting room booking and utilization service written in Go using
**only the standard library**. It builds and tests fully offline (no module
dependencies, no network access), and ships with a JSON command-line interface.

## Features

- **Room registration** — register rooms by id, name and capacity.
- **Booking by time range** — reserve a room over a half-open interval
  `[Start, End)`.
- **Cancellation** — cancel a reservation; duplicate cancels are idempotent.
- **Availability query** — list rooms free during a requested interval.
- **Utilization by date** — per-room and aggregate utilization for a calendar
  date, with an optional operating-hours window and timezone support.

## Design rules (public interface guarantees)

The public methods are explicit about three tricky areas:

### Time boundaries

- A reservation occupies a **half-open** interval `[Start, End)`: the `End`
  instant is exclusive. Two reservations that meet at a boundary
  (one ends at `10:00`, the next starts at `10:00`) **do not conflict**.
- `Start` must be strictly before `End`; a zero-length or inverted range is
  rejected as invalid input.
- Utilization is computed by **clamping** each reservation to the queried
  day's window, so reservations that span midnight or fall partly outside the
  window are prorated correctly.

### Duplicate / retry requests

- **Idempotency key** (`clientRequestId`): a repeated `book` with the same key
  returns the prior reservation instead of creating a new one.
- **Content dedup**: even without a key, a `book` that exactly matches an
  existing active reservation (same room, start, end, booker) returns that
  reservation instead of erroring.
- **Cancel idempotency**: cancelling an already-cancelled reservation returns
  its current state without error, absorbing duplicate cancel requests.

### Invalid input

Each error is a typed sentinel so callers can branch on category with
`errors.Is`:

| Sentinel                     | Meaning                                           |
| ---------------------------- | ------------------------------------------------- |
| `ErrInvalidInput`            | Missing/malformed fields (id, name, booker, etc.) |
| `ErrInvalidTimeRange`        | `start` missing or `start >= end`                |
| `ErrInvalidDate`             | Date not `YYYY-MM-DD`                             |
| `ErrInvalidOperatingWindow`  | `open`/`close` malformed or `open >= close`       |
| `ErrRoomNotFound`            | Referenced room does not exist                    |
| `ErrRoomAlreadyExists`       | Duplicate room id on registration                 |
| `ErrReservationNotFound`     | Referenced reservation does not exist            |
| `ErrConflict`                | Time overlap with an existing reservation         |

`ErrConflict` is carried by `*ConflictError`, which exposes the conflicting
reservation's id; use `errors.As` to obtain it.

## Project layout

```
meetingroom/
├── go.mod
├── README.md
├── examples/
│   ├── batch.json      # a sample batch of commands
│   └── output.json     # the corresponding output (regenerable)
├── booking/            # the in-memory service (public API)
│   ├── booking.go      # core types (Room, Reservation)
│   ├── errors.go       # sentinel errors + ConflictError
│   ├── api.go          # request/response DTOs
│   ├── service.go      # Service implementation
│   └── service_test.go # full behavior + concurrency tests
└── cmd/meetingroom/    # JSON batch CLI
    ├── main.go
    └── main_test.go
```

## Build & verify

All commands work offline. From the project root:

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

## Command-line usage

`meetingroom` reads a **batch** — a JSON array (or single object) of commands —
from an input file or stdin, replays them against a fresh in-memory service,
and writes a JSON array of results (aligned by `index` to the input).

```sh
go run ./cmd/meetingroom -in examples/batch.json -out examples/output.json
# or via stdin/stdout:
cat examples/batch.json | go run ./cmd/meetingroom
```

### Command reference

| op             | fields                                                                              |
| -------------- | ----------------------------------------------------------------------------------- |
| `register-room`| `id`, `name`, `capacity`                                                             |
| `book`         | `roomId`, `start`, `end`, `booker`, `clientRequestId?`                               |
| `cancel`       | `reservationId`                                                                     |
| `available`    | `start`, `end`                                                                       |
| `utilization`  | `date` (`YYYY-MM-DD`), `location?`, `open?` (`HH:MM`), `close?` (`HH:MM`, `24:00`)  |

Times are RFC3339 strings (e.g. `2026-08-15T09:00:00Z`). Each result carries
`ok`; on failure it carries an `error` string instead of the payload.

### A short example

`examples/batch.json` (abridged):

```json
[
  {"op":"register-room","id":"atlas","name":"Atlas","capacity":12},
  {"op":"register-room","id":"borealis","name":"Borealis","capacity":8},
  {"op":"register-room","id":"atlas","name":"Dup","capacity":1},
  {"op":"book","roomId":"atlas","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","booker":"alice","clientRequestId":"k1"},
  {"op":"book","roomId":"atlas","start":"2026-08-15T09:00:00Z","end":"2026-08-15T10:00:00Z","booker":"alice","clientRequestId":"k1"},
  {"op":"book","roomId":"atlas","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:30:00Z","booker":"bob"},
  {"op":"book","roomId":"atlas","start":"2026-08-15T10:00:00Z","end":"2026-08-15T11:00:00Z","booker":"carol"},
  {"op":"book","roomId":"borealis","start":"2026-08-15T09:00:00Z","end":"2026-08-15T17:00:00Z","booker":"dave"},
  {"op":"available","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:00:00Z"},
  {"op":"cancel","reservationId":"res-000001"},
  {"op":"cancel","reservationId":"res-000001"},
  {"op":"available","start":"2026-08-15T09:30:00Z","end":"2026-08-15T10:00:00Z"},
  {"op":"utilization","date":"2026-08-15","location":"UTC","open":"09:00","close":"18:00"}
]
```

What this demonstrates, command by command:

1. Register `atlas` and `borealis` → **ok**.
2. Register `atlas` again → **error** (`room already exists`).
3. Book `atlas` 09:00–10:00 for `alice` with idempotency key `k1` → **ok**, id
   `res-000001`.
4. Retry the same book with key `k1` → **ok**, same id `res-000001` (idempotent,
   no new reservation).
5. Book `atlas` 09:30–10:30 for `bob` → **error** (`time conflict with
   reservation res-000001`).
6. Book `atlas` 10:00–11:00 for `carol` → **ok**, `res-000002` (adjacent to
   alice's booking, boundary is allowed).
7. Book `borealis` 09:00–17:00 for `dave` → **ok**, `res-000003`.
8. Available 09:30–10:00 → **none** (`atlas` overlaps alice; `borealis`
   overlaps dave).
9. Cancel `res-000001` → **ok**, `cancelled: true`.
10. Cancel `res-000001` again → **ok**, still `cancelled: true` (idempotent).
11. Available 09:30–10:00 again → **`[atlas]`** (alice's booking is gone;
    carol's 10:00–11:00 does not overlap 09:30–10:00).
12. Utilization 2026-08-15, window 09:00–18:00 (9h = 32400s/room):
    - `atlas`: booked 1h (carol), utilization 1/9 ≈ 0.1111
    - `borealis`: booked 8h (dave), utilization 8/9 ≈ 0.8889
    - aggregate: 32400 / 64800 = **0.5**

## Using the library directly

```go
import (
    "time"
    "github.com/fddhuwenjie/hwj-go-0003/booking"
)

func mustParse(s string) time.Time {
    t, err := time.Parse(time.RFC3339, s)
    if err != nil {
        panic(err)
    }
    return t
}

s := booking.NewService()
s.RegisterRoom(booking.RegisterRoomRequest{ID: "A", Name: "Atlas", Capacity: 10})
r, err := s.Book(booking.BookRequest{
    RoomID: "A",
    Start:  mustParse("2026-08-15T09:00:00Z"),
    End:    mustParse("2026-08-15T10:00:00Z"),
    Booker: "alice",
})
// ...
```

A fixed clock is available for deterministic tests:
`booking.NewServiceWithClock(func() time.Time { return t })`.

## Notes & limitations

- State is **in-memory only**; it is lost when the process exits. The CLI's
  batch model keeps one process / one service / a full workflow together.
- The timezone database is embedded via `time/tzdata`, so `time.LoadLocation`
  works without a system tz database (fully offline).
- Reservation ids are generated as `res-NNNNNN` from an in-process counter;
  they are unique within a single service instance.
