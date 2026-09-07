# Diagrams of jetway internals

These 2 companion documents help anyone who traces a message through this
codebase or asks why a booking is in its current state:

- **[Message flows](flows.md)** has sequence diagrams for every conversation
  jetway holds. The conversations are sells and their replies in both
  dialects, cancellations, availability, schedule changes, movements,
  tickets, and ground handling. The cancellations include the ones that
  cross other messages on the network.
- **[State machines](states.md)** has the status vocabularies and their legal
  transitions. These cover segment action codes, record status, the message
  pipeline, and queue items.

Everything here is drawn from the code as it is, and not as it was planned.
Where a diagram shows a guard, such as "a dead segment must not be confirmed
back to life", a regression test holds it. The wire examples in the package
documentation are the authority on byte-level formats. These pages are the
authority on who talks to whom, in what order, and what a status is allowed
to become.
