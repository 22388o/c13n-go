package rpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

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
	resolvedTimeNs := payment.Htlcs[len(payment.Htlcs)-1].ResolveTimeNs
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

	return &pb.Payment{
		Hash:              payment.Hash,
		Preimage:          payment.Preimage,
		AmtMsat:           uint64(payment.Value.Msat()),
		CreatedTimestamp:  createdTime,
		ResolvedTimestamp: resolvedTime,
		PayReq:            payment.PaymentRequest,
		State:             state,
		PaymentIndex:      payment.PaymentIndex,
	}, nil
}

// NewPaymentServiceServer initializes a new payment service.
func NewPaymentServiceServer(app *app.App) pb.PaymentServiceServer {
	return &paymentServiceServer{
		Log: slog.NewLogger("payment-service"),
		App: app,
	}
}
