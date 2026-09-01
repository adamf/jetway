# Jetway internals, drawn

Two companion documents for anyone tracing a message through this codebase
or wondering why a booking is in the state it is in:

- **[Message flows](flows.md)** — sequence diagrams for every conversation
  jetway holds: sells and their replies in both dialects, cancellations
  (including the ones that cross other messages on the network),
  availability, schedule changes, movements, tickets, and the ground story.
- **[State machines](states.md)** — the status vocabularies and their legal
  transitions: segment action codes, record status, the message pipeline,
  and queue items.

Everything here is drawn from the code as it is, not as it was planned;
where a diagram shows a guard ("a dead segment must not be confirmed back
to life"), there is a regression test holding it. The wire examples in the
package documentation are the authority on byte-level formats; these pages
are the authority on who talks to whom, in what order, and what a status
is allowed to become.
