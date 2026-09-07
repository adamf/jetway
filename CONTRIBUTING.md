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

## Engineering principles

**Never lose a message.** Any change to the pipeline must preserve 2
properties. Raw bytes are durable before any stage interprets them. A failure
after that point leaves a replayable message instead of a gap. If you cannot
parse part of a message, attach that part to the record as an unparsed fragment.
Do not drop it.

**Decoders report deviations and continue.** The only hard failures are inputs
with no usable structure. A gateway that rejects malformed traffic loses
messages that a partner considers delivered.

**Test the property that the code guarantees.** Use these 2 suites as models:

- `pkg/store/store_test.go` runs the same assertions against both backends.
  A test that runs only against memory does not exercise optimistic concurrency
  where it is implemented.
- `pkg/edifact` fuzzes round-trip stability. It has exposed 6 defects so far.
  No hand-written test would have covered any of them.

**Comments explain the reason for the code.** The code itself shows what it
does. Write a comment where the obvious approach is wrong. The comments on
locator allocation, date resolution and the EDIFACT syntax-version rescan are
the examples to read first.

## Adding to a message grammar

Prefer an extension of a profile to a change of a default. `airimp.Profile` and
`padis.Profile` exist for carrier dialects. With them, a carrier's dialect does
not need a fork. Change the default only when the current behaviour is wrong for
every carrier. State in the commit message which carrier's traffic showed the
problem.

If you add an element or segment, add a round-trip test. The test must show
that the encoded output parses back to the intended structure.

## Specifications

AIRIMP and the PADIS directories are IATA publications, and they are the
normative source. **Do not paste specification text, tables or code lists into
this repository.** Implement the behaviour and describe it in your own words.
Cite the section. A reader with the manual can then check it.

If you have access to a specification and find this implementation wrong, that
is the most valuable bug report you can file. Describe the divergence in your
own words and cite where to look.

## Pull request checklist

- `make check` passes.
- New behaviour has a test that fails without the change.
- For codec changes, run the fuzz corpus with `make fuzz`.
- Fixtures contain no personal data, no production record locators, and no
  captured partner traffic. Synthesise fixture data.

## Reporting a bug in a decoder

The most useful report is a failing test case. A captured message is second
best. Replace anything that identifies a person, and change the names.
