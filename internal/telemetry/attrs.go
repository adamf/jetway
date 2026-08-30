package telemetry

import "go.opentelemetry.io/otel/attribute"

// The jetway attribute vocabulary.
//
// Two audiences read these. An operator asks which link is slow, what is
// failing, and how deep the backlog is. The commercial side asks how often a
// carrier refuses, how much of a booking sells without having to ask, how long
// a carrier takes to answer, and what ancillary revenue was issued against
// what. Both are answerable from the same spans, which is why there is one
// vocabulary rather than an operational one and a business one that drift.
//
// Nothing here carries passenger data. Spans leave the building for a collector
// somebody else very likely operates, and they outlive the intention to keep
// them. Locators and carrier codes are fine: they are the identifiers needed to
// follow a booking, and they mean nothing without the record they point at.
// Names, contacts, documents and frequent flyer numbers are not, and there is a
// test that the booking span carries no passenger name.
const (
	// Link and message shape: what arrived, from whom, on what.
	AttrPeer        = attribute.Key("jetway.peer")
	AttrCarrier     = attribute.Key("jetway.carrier")
	AttrFormat      = attribute.Key("jetway.format")
	AttrTransport   = attribute.Key("jetway.transport")
	AttrMessageID   = attribute.Key("jetway.message.id")
	AttrMessageKind = attribute.Key("jetway.message.kind")
	AttrMessageSize = attribute.Key("jetway.message.bytes")
	AttrDirection   = attribute.Key("jetway.direction")
	AttrStatus      = attribute.Key("jetway.status")
	AttrDuplicate   = attribute.Key("jetway.duplicate")

	// The record a message touched.
	AttrLocator      = attribute.Key("jetway.record.locator")
	AttrRecordID     = attribute.Key("jetway.record.id")
	AttrSegmentCount = attribute.Key("jetway.record.segments")
	AttrPaxCount     = attribute.Key("jetway.record.passengers")
	AttrInterline    = attribute.Key("jetway.record.interline")

	// Selling. The commercial questions live here: how much goes out under
	// free sale rather than as a request, and what the carrier said back.
	AttrSeats      = attribute.Key("jetway.seats")
	AttrFreeSale   = attribute.Key("jetway.free_sale")
	AttrOutcome    = attribute.Key("jetway.outcome")
	AttrActionCode = attribute.Key("jetway.action_code")
	AttrChannel    = attribute.Key("jetway.channel")

	// Documents and ancillary revenue. RFIC is the revenue category, which is
	// the point of carrying it.
	AttrDocumentType   = attribute.Key("jetway.document.type")
	AttrDocumentNumber = attribute.Key("jetway.document.number")
	AttrCouponCount    = attribute.Key("jetway.document.coupons")
	AttrRFIC           = attribute.Key("jetway.document.rfic")
	AttrRFISC          = attribute.Key("jetway.document.rfisc")
	AttrAmount         = attribute.Key("jetway.document.amount")
	AttrCurrency       = attribute.Key("jetway.document.currency")

	// Work and disagreement.
	AttrQueue       = attribute.Key("jetway.queue")
	AttrQueueCode   = attribute.Key("jetway.queue.code")
	AttrQueuePlaced = attribute.Key("jetway.queue.placed")
	AttrDivergence  = attribute.Key("jetway.divergence")
	AttrUnreachable = attribute.Key("jetway.carriers.unreachable")
	AttrNotified    = attribute.Key("jetway.carriers.notified")

	// Diagnostics the decoder raised, so a dialect problem is visible as a
	// count before it is visible as a support ticket.
	AttrDiagnostics = attribute.Key("jetway.diagnostics")
	AttrDecodeError = attribute.Key("jetway.decode_error")
)

// Outcome values for AttrOutcome. They are the commercial answer to a sell,
// collapsed to the four cases anybody reports on.
const (
	OutcomeConfirmed  = "confirmed"
	OutcomeWaitlisted = "waitlisted"
	OutcomeRefused    = "refused"
	OutcomePending    = "pending"
)

// Channel values for AttrChannel: how a booking reached this node.
const (
	ChannelAPI     = "api"
	ChannelNDC     = "ndc"
	ChannelTypeB   = "typeb"
	ChannelEDIFACT = "edifact"
)
