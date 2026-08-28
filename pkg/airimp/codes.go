package airimp

import "github.com/adamf/jetway/pkg/rescode"

// The reservation status vocabulary lives in pkg/rescode because EDIFACT uses
// the same codes. These aliases keep it reachable through this package for
// callers working in teletype terms.
type (
	// ActionCode is a two-letter reservation action or status code.
	ActionCode = rescode.ActionCode
	// Category classifies what a code does.
	Category = rescode.Category
	// CodeInfo describes an action code.
	CodeInfo = rescode.CodeInfo
)

const (
	CatUnknown = rescode.CatUnknown
	CatRequest = rescode.CatRequest
	CatReply   = rescode.CatReply
	CatHolding = rescode.CatHolding
	CatCancel  = rescode.CatCancel
	CatAdvice  = rescode.CatAdvice
)

// Codes is the interline action and status code vocabulary.
var Codes = rescode.Codes

// ReplyTo returns the holding status a requester should record after receiving
// reply, and whether reply is a reply code at all.
func ReplyTo(reply ActionCode) (ActionCode, bool) { return rescode.ReplyTo(reply) }
