package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// A refund turns the open coupons of a record's tickets to refunded and
// dates the document; a coupon already lifted keeps its value used; a
// second refund finds nothing; an unticketed record has nothing to refund.
func TestRefundTurnsOpenCouponsAndLeavesUsedOnes(t *testing.T) {
	gw, _ := cancelNode(t)
	ctx := context.Background()
	interlineRecord(t, gw, "RFD001")
	if _, err := gw.Refund(ctx, "RFD001", RefundOptions{By: "adam", Reason: "plans changed"}); !errors.Is(err, ErrNothingToRefund) {
		t.Fatalf("refund before ticketing: %v", err)
	}
	if _, err := gw.IssueTickets(ctx, "RFD001", IssueOptions{AirlineCode: "125", IssuedBy: "adam"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := gw.Store.GetPNR(ctx, "RFD001")
	if len(rec.Tickets) == 0 || len(rec.Tickets[0].Coupons) < 2 {
		t.Fatalf("test record: %+v", rec.Tickets)
	}
	// The outbound was flown.
	rec.Tickets[0].Coupons[0].Status = pnr.CouponLifted
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}
	after, err := gw.Refund(ctx, "RFD001", RefundOptions{By: "adam", Reason: "plans changed"})
	if err != nil {
		t.Fatal(err)
	}
	tk := after.Tickets[0]
	if tk.RefundedAt == nil || tk.Coupons[0].Status != pnr.CouponLifted || tk.Coupons[1].Status != pnr.CouponRefunded {
		t.Errorf("after refund: %+v", tk)
	}
	if !tk.Refunded() {
		t.Error("a document with no open coupon and a refund date is refunded")
	}
	events, _ := gw.Store.Events(ctx, after.ID)
	found := false
	for _, e := range events {
		if e.Type == "refund" && e.Actor == "adam" {
			found = true
		}
	}
	if !found {
		t.Errorf("no refund event: %+v", events)
	}
	if _, err := gw.Refund(ctx, "RFD001", RefundOptions{By: "adam"}); !errors.Is(err, ErrNothingToRefund) {
		t.Errorf("a second refund: %v", err)
	}
	if _, err := gw.Refund(ctx, "NOPE00", RefundOptions{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown record: %v", err)
	}
}
