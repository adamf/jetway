# Working on Jetway

An open-source messaging gateway for airline and GDS reservation traffic. Go,
MIT, `github.com/adamf/jetway`.

## The rule that matters most: say what you actually know

Most of the formats here are defined in paid IATA publications that were not
bought. That is not a reason to guess quietly — it is a reason to be explicit
about which layer is which.

| Layer | Standing |
| --- | --- |
| `pkg/edifact` (ISO 9735), `pkg/edifact` CONTRL, `pkg/matip` (RFC 2351) | **Specified.** The documents are public. Conformance can be checked and is. |
| `pkg/typeb` limits, PDM | **Specified.** IATA's Type B whitepaper is free. |
| `pkg/padis` | **Partly.** The PNRGOV implementation guide is free and checking against it fixed four real bugs; the PNRGOV push itself is **specified**, tested against the guide's worked example verbatim. PAOREQ/PAORES remain inferred. |
| `pkg/airimp`, `pkg/avs`, `pkg/ssim` | **Inferred.** Profiles, not conformance. AIRIMP and SSIM are paywalled. The chapter 7 file layout in `pkg/ssim/file.go` is the one two independent open-source parsers implement identically and is tested against one's sample records; still a reproduction, not the manual. |
| `pkg/ndc` | **Specified.** Schemas and real carrier examples are public. |
| `pkg/mvt` | **Inferred, closely.** AHM 780/781 is paid, but OAG publishes the element tables and verbatim examples this package is built and tested against. |
| `pkg/dcs` PSM, PTM, LDM, CPM | **Inferred, closely.** RP 1715/1718 and AHM 583/587 are paid, but airports and handlers reproduce the practices' own worked examples; the parsers are tested against those verbatim. |
| `pkg/dcs` PFS, ETL | **Inferred.** No free reproduction of RP 1719/1719c was found. The layouts follow the PNL family; the category vocabulary is public. |
| `pkg/aftn` | **Specified.** ICAO Annex 10 Volume II is public; the envelope is tested against the Annex's own example. |
| `pkg/ats` | **Specified via free reproductions.** Doc 4444 is sold, but the FAA's ICAO flight planning guidance reproduces the message forms with verbatim examples the parser is tested against. |
| `pkg/acars` | **Inferred, closely.** ARINC 620 is sold; OAG publishes the element table and verbatim OOOI examples this package is built and tested against. |
| `pkg/dcs` load control | **Method specified, data representative.** The AHM 560/565 index arithmetic is the published method; `DefaultFleet` is rounded type-class data, not any operator's. |
| `pkg/fare` | **Structure inferred, data none.** Fare basis, rules, taxes and pricing follow how ATPCO filings and tickets work; the filings themselves are licensed and the package carries no fare. Callers supply a tariff. |
| `pkg/paxlst` | **Specified.** The WCO/IATA/ICAO PAXLST implementation guide is public; the package follows its segment tables and is tested against its worked examples verbatim. SSR DOCS layout is inferred from published entry formats. |
| `pkg/iatci` | **Inferred, closely.** DCQCKI/DCRCKA and their segments follow the PADIS release 01.1 structures as mirrored publicly by EDI schema vendors, element by element; the IATCI Implementation Guide (members only) was not consulted, so usage is this package's profile. |
| `pkg/inventory` | **Method specified.** Serial nested class authorisations are the textbook leg-based inventory control, and EMSR-b (Belobaba; Talluri and van Ryzin ch. 2) sets them from a forecast; the numbers are the caller's. The EMSR-b tests check against the normal table, not the code. |

When you implement something in the inferred category, say so in the package
doc, make it an extensible `Profile`, and keep unrecognised input verbatim.
Never write a doc comment that implies conformance you cannot demonstrate.

`docs/roadmap.md` has a section naming each paid document and what its absence
costs. Add to it rather than scattering another caveat: the point of collecting
them is that they are one procurement decision, not six unrelated apologies.
The **AIRIMP divide message** is the most expensive single absence — it is why a
split booking cannot be advised to its carriers.

When public sources **disagree** about a rule — as they do for the ticket check
digit — implement one, make it advisory rather than a gate, and document the
disagreement. Rejecting valid input on an uncertain rule is worse than
accepting invalid input.

## Search for a free source before writing "blocked"

Every single time this has been done, it paid:

- The free **PNRGOV guide** corrected four `pkg/padis` bugs whose tests passed
  because the tests encoded the same guess as the code — and later unblocked
  half the divide feature, because `EQN` and `RCI` describe how PADIS
  represents a split.
- The free **EMD guide** corrected three errors in a guessed coupon status list
  and supplied the open/interim/final structure the guess had missed.
- The free **CONTRL UNSM** made that whole layer checkable rather than inferred.
- The free **Type B whitepaper** produced the 4 KB cap and PDM, neither
  implemented.

Look for: the free table of contents (it names messages and sections, which
narrows what is genuinely unknown), government and regulator implementation
guides, vendor and airport-authority reproductions, and adjacent free standards
sharing a vocabulary. Then state precisely what remains unknown. "We do not
have the manual" was doing too much work until it was replaced by a table.

## Invariants — do not break these

- **Capture precedes interpretation.** Raw bytes are durable before anything
  parses them. This is what makes replay-after-parser-fix possible.
- **Never regenerate raw bytes from a parse.** `Message.Raw` is the evidence. A
  re-encode is a different artefact. `typeb.MarkPossibleDuplicate` edits bytes
  textually for exactly this reason.
- **Nothing undecodable is discarded.** Unknown lines become fragments;
  undecodable messages go to the DLQ with bytes intact.
- **PNR state is an event-sourced projection** with optimistic concurrency. A
  write carries the version it read; on `ErrConflict`, re-read and reapply.
- **Wire syntax is exact; message grammar is a profile.**
- **No personal or payment data in the message log.** There is no encryption at
  rest. `pkg/ndc` refuses payloads carrying card numbers *before* capture.

## Testing discipline

**The trap this repo has fallen into twice:** tests that encode the same guess
as the implementation prove nothing. Four PADIS bugs and seven EDIFACT bugs
both passed their own tests. Guard against it by:

- Checking against an **external** artefact where one exists — a published
  spec, a real captured message, another implementation's corpus.
- **Fuzzing round trips.** `pkg/edifact` and `pkg/typeb` both have
  `FuzzRoundTrip`, and each found real defects of the same shape: *a decoder
  depending on its own output*. Add one for any new codec.
- Writing fixtures **by hand** from the spec, not by calling your own builder.
  A hand-built UNB fixture caught an off-by-one in element position that a
  round trip would have missed.

**The scenario suite is the other half.** `go test ./internal/scenario` drives
end-to-end exchanges through the real assembly from `pkg/node` -- the same
one `jetwayd` builds -- with the demo carriers on real TCP. `cmd/jetwayload`
runs the identical scenarios concurrently and reports latency. Add a scenario
for anything that crosses a link; unit tests do not catch what only shows up
with a partner on the other end. Writing it found four real defects, including
a booking whose lowercase agent name made every EDIFACT request unencodable.

A new test does not count until it has been **watched to fail** against the old
behaviour. Every regression test added for the lookup, sweep and scenario work
was checked that way, and it is cheap: revert the fix, run the test, restore.

Store changes must pass the conformance suite against **both** backends:

```sh
# throwaway postgres; the socket path must be short or it will not start
initdb -D /tmp/jwpg -U postgres --auth=trust
pg_ctl -D /tmp/jwpg -o "-p 55432 -k /tmp -c listen_addresses=127.0.0.1" -l /tmp/jwpg/log start
createdb -h 127.0.0.1 -p 55432 -U postgres jetway_test
JETWAY_TEST_DSN="postgres://postgres@127.0.0.1:55432/jetway_test?sslmode=disable" go test ./...
```

Without `JETWAY_TEST_DSN` the Postgres backend is skipped silently, and the
in-memory store has drifted from it before.

## Running it

```sh
go run ./cmd/jetwayd        # gateway + three simulated carriers, console on :8080
go run ./cmd/jetwayctl decode captured.tty
```

The demo carriers dial loopback **inside the process**, which is why a container
host runs this unchanged and a function platform cannot.

## Layering

Everything importable lives under `pkg/`, in two layers. The codec packages
(`typeb`, `edifact`, `airimp`, `padis`, `avs`, `ssim`, `ndc`, `matip`, `pnr`,
`rescode`, `avail`, `pnl`, `baggage`, `mvt`, `dcs`, `aftn`, `ats`, `acars`) must not import the application packages (`gateway`,
`store`, `node`, `queue`, `ingress`, `egress`, `transport`, `config`, `demo`,
`api`, `metrics`, `telemetry`, `spool`, `ulid`). The application packages were
promoted out of `internal/` on 2026-08-31 so external consumers -- the world
simulator first among them -- can embed a node; treat their exported surface as
API now, not as private plumbing. Only `internal/scenario` (the test harness)
stays internal.

## Conventions

- Comments explain **why**, not what. If a line needs a comment saying what it
  does, rewrite the line.
- Go doc comments on every exported symbol, in prose, saying what the thing is
  for and what breaks without it.
- British spelling in prose (`normalise`, `behaviour`).
- Commit messages have real bodies explaining the reasoning, wrapped at ~72
  characters. Look at `git log` before writing one.
- Migrations are dense-numbered from 1 and never edited once applied anywhere.

## Deploying

`flyctl deploy` builds from the working directory, so the demo can end up ahead
of GitHub. Push first.

Adam wants commits **pushed** as soon as the work is verified — no asking, no
feature branch, straight to `main`, while the project is pre-release.
