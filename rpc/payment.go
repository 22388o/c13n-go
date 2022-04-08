package rpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightningnetwork/lnd/lnrpc"

	"github.com/c13n-io/c13n-go/app"
	"github.com/c13n-io/c13n-go/lnchat"
	"github.com/c13n-io/c13n-go/model"
	pb "github.com/c13n-io/c13n-go/rpc/services"
	"github.com/c13n-io/c13n-go/slog"
)

type paymentServiceServer struct {
	Log *slog.Logger

	App *app.App

	pb.UnimplementedPaymentServiceServer
}

func (s *paymentServiceServer) logError(err error) error {
	if err != nil {
		s.Log.Errorf("%+v", err)
	}
	return err
}

// Interface implementation

// CreateInvoice creates and returns an invoice for the specified amount
// with the specified memo and expiry time.
func (s *paymentServiceServer) CreateInvoice(ctx context.Context, req *pb.CreateInvoiceRequest) (*pb.CreateInvoiceResponse, error) {

	inv, err := s.App.CreateInvoice(ctx,
		req.Memo, int64(req.AmtMsat), req.Expiry, req.Private)
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	resp, err := invoiceModelToRPCInvoice(inv)
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	return &pb.CreateInvoiceResponse{
		Invoice: resp,
	}, nil
}

// LookupInvoice retrieves an invoice and returns it.
func (s *paymentServiceServer) LookupInvoice(ctx context.Context, req *pb.LookupInvoiceRequest) (*pb.LookupInvoiceResponse, error) {
	inv, err := s.App.LookupInvoice(ctx, req.GetPayReq())
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	resp, err := invoiceModelToRPCInvoice(inv)
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	return &pb.LookupInvoiceResponse{
		Invoice: resp,
	}, nil
}

// Pay performs a payment and returns it.
func (s *paymentServiceServer) Pay(ctx context.Context,
	req *pb.PayRequest) (*pb.PayResponse, error) {

	paymentOptions := lnchat.PaymentOptions{
		FeeLimitMsat: req.Options.GetFeeLimitMsat(),
	}
	payment, err := s.App.SendPayment(ctx,
		req.GetAddress(), int64(req.GetAmtMsat()), req.GetPayReq(),
		paymentOptions, nil)
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	resp, err := newPayment(payment)
	if err != nil {
		return nil, associateStatusCode(s.logError(err))
	}

	return &pb.PayResponse{
		Payment: resp,
	}, nil
}

func newPayment(payment *model.Payment) (*pb.Payment, error) {
	var err error
	var createdTime, resolvedTime *timestamppb.Timestamp
	resolvedTimeNs := int64(0)
	// Assign resolvedTimeNs to the latest succeeded htlc resolve timestamp
	for _, h := range payment.Htlcs {
		if h.Status == lnrpc.HTLCAttempt_SUCCEEDED && h.ResolveTimeNs > resolvedTimeNs {
			resolvedTimeNs = h.ResolveTimeNs
		}
	}
	if resolvedTimeNs > 0 {
		ts := time.Unix(0, resolvedTimeNs)
		if resolvedTime, err = newProtoTimestamp(ts); err != nil {
			return nil, fmt.Errorf("marshal error: invalid timestamp: %v", err)
		}
	}
	if payment.CreationTimeNs > 0 {
		ts := time.Unix(0, payment.CreationTimeNs)
		if createdTime, err = newProtoTimestamp(ts); err != nil {
			return nil, fmt.Errorf("marshal error: invalid timestamp: %v", err)
		}
	}

	var state pb.PaymentState
	switch payment.Status {
	case lnchat.PaymentUNKNOWN:
		state = pb.PaymentState_PAYMENT_UNKNOWN
	case lnchat.PaymentINFLIGHT:
		state = pb.PaymentState_PAYMENT_INFLIGHT
	case lnchat.PaymentSUCCEEDED:
		state = pb.PaymentState_PAYMENT_SUCCEEDED
	case lnchat.PaymentFAILED:
		state = pb.PaymentState_PAYMENT_FAILED
	default:
		return nil, fmt.Errorf("marshal error: invalid payment state: %v", err)
	}

	htlcs, err := newPaymentHTLCs(payment.Htlcs)
	if err != nil {
		return nil, err
	}

	return &pb.Payment{
		Hash:              payment.Hash,
		Preimage:          payment.Preimage,
		AmtMsat:           uint64(payment.Value.Msat()),
		CreatedTimestamp:  createdTime,
		ResolvedTimestamp: resolvedTime,
		PayReq:            payment.PaymentRequest,
		State:             state,
		PaymentIndex:      payment.PaymentIndex,
		HTLCs:             htlcs,
	}, nil
}

func newPaymentHTLCs(htlcs []lnchat.HTLCAttempt) ([]*pb.PaymentHTLC, error) {
	pbHTLCs := make([]*pb.PaymentHTLC, len(htlcs))
	for i, h := range htlcs {
		route, err := newPaymentHTLCRoute(&h.Route)
		if err != nil {
			continue
		}
		var attemptTime, resolveTime *timestamppb.Timestamp
		if h.AttemptTimeNs > 0 {
			ts := time.Unix(0, h.AttemptTimeNs)
			if attemptTime, err = newProtoTimestamp(ts); err != nil {
				return nil, fmt.Errorf("marshal error: invalid timestamp: %v", err)
			}
		}
		if h.ResolveTimeNs > 0 {
			ts := time.Unix(0, h.ResolveTimeNs)
			if resolveTime, err = newProtoTimestamp(ts); err != nil {
				return nil, fmt.Errorf("marshal error: invalid timestamp: %v", err)
			}
		}

		var state pb.HTLCState
		switch h.Status {
		case lnrpc.HTLCAttempt_IN_FLIGHT:
			state = pb.HTLCState_HTLC_IN_FLIGHT
		case lnrpc.HTLCAttempt_SUCCEEDED:
			state = pb.HTLCState_HTLC_SUCCEEDED
		case lnrpc.HTLCAttempt_FAILED:
			state = pb.HTLCState_HTLC_FAILED
		}

		pbHTLCs[i] = &pb.PaymentHTLC{
			Route:            route,
			AttemptTimestamp: attemptTime,
			ResolveTimestamp: resolveTime,
			State:            state,
			Preimage:         string(h.Preimage),
		}
	}

	return pbHTLCs, nil
}

func newPaymentHTLCRoute(route *lnchat.Route) (*pb.PaymentRoute, error) {
	hops, err := newPaymentHTLCRouteHops(route.Hops)
	if err != nil {
		return nil, err
	}

	return &pb.PaymentRoute{
		Hops:          hops,
		TotalTimelock: route.TimeLock,
		RouteAmtMsat:  route.Amt.Msat(),
		RouteFeesMsat: route.Fees.Msat(),
	}, nil
}

func newPaymentHTLCRouteHops(hops []lnchat.RouteHop) ([]*pb.PaymentHop, error) {
	resp := make([]*pb.PaymentHop, len(hops))
	for i, hop := range hops {
		resp[i] = &pb.PaymentHop{
			ChanId:           hop.ChannelID,
			HopAddress:       hop.NodeID.String(),
			AmtToForwardMsat: hop.AmtToForward.Msat(),
			FeeMsat:          int64(hop.Fees.Msat()),
		}
	}
	return resp, nil
}

// NewPaymentServiceServer initializes a new payment service.
func NewPaymentServiceServer(app *app.App) pb.PaymentServiceServer {
	return &paymentServiceServer{
		Log: slog.NewLogger("payment-service"),
		App: app,
	}
}
