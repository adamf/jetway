# Contributing

## Getting set up

```sh
go test ./...        # the suite; Postgres tests skip without a DSN
make check           # format, vet, test
```

To include the Postgres store conformance tests:

```sh
createdb jetway_test
JETWAY_TEST_DSN="postgres://$USER@localhost/jetway_test?sslmode=disable" go test ./...
```

## What good looks like here

**Never lose a message.** Any change to the pipeline must preserve the property
that raw bytes are durable before anything interprets them, and that a failure
after that point leaves a replayable message rather than a gap. If you find
yourself dropping something you cannot parse, attach it to the record as an
unparsed fragment instead.

**Diagnostics, not exceptions.** Decoders report deviations and keep going. The
only hard failures are input with no usable structure. A gateway that rejects
malformed traffic loses messages that a partner considers delivered.

**Test the property, not the implementation.** The two suites worth imitating:

- `internal/store/store_test.go` runs the same assertions against both backends.
  A test that only runs against memory does not exercise optimistic concurrency
  where it is actually implemented.
- `pkg/edifact` fuzzes round-trip stability. Six real defects so far, each one
  a case no hand-written test would have thought of.

**Explain why in comments, not what.** The code says what it does. Comments
should say why it is that way, especially where the obvious approach is wrong —
locator allocation, date resolution and the EDIFACT syntax-version rescan are
the examples to read first.

## Adding to a message grammar

Prefer extending a profile over changing a default. `airimp.Profile` and
`padis.Profile` exist so a carrier's dialect does not need a fork. Change the
default only when the current behaviour is wrong for everyone, and say in the
commit message which carrier's traffic showed it.

If you add an element or segment, add a round-trip test: what you build must
parse back to what you meant.

## Specifications

AIRIMP and the PADIS directories are IATA publications and are the normative
source. **Do not paste specification text, tables or code lists into this
repository.** Implement the behaviour, describe it in your own words, and cite
the section so a reader with the manual can check it.

If you have access to a specification and find this implementation wrong, that
is the most valuable bug report you can file. Describe the divergence in your
own words and cite where to look.

## Before you open a pull request

- `make check` passes.
- New behaviour has a test that fails without the change.
- Codec changes run the fuzz corpus: `make fuzz`.
- No personal data, real record locators, or captured partner traffic in
  fixtures. Synthesise them.

## Reporting a bug in a decoder

The most useful report is a failing test case. A captured message is second best
— with anything identifying replaced, and the names changed.
