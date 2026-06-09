package plaid

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	plaid "github.com/plaid/plaid-go/v31/plaid"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// transferUserLegalName is sent on the authorization. The current charge/payout
// model does not carry the account holder's legal name, so a placeholder is
// used; threading the real name through is part of completing the Plaid
// integration (see package note).
const transferUserLegalName = "Account Holder"

// CreateCharge debits a customer's bank account via an ACH transfer. Plaid
// requires a two-step flow: authorize, then create. The token id is expected to
// encode the Plaid access token and account id as "accessToken|accountId".
//
// PCI-DSS: only Plaid tokens are handled; bank/routing numbers never reach here.
func (c *Client) CreateCharge(ctx context.Context, req processor.ChargeRequest) (processor.ChargeResult, error) {
	transfer, err := c.transfer(ctx, req.Mode, plaid.TRANSFERTYPE_DEBIT, req.ProcessorMethodID, req.Amount, req.Description)
	if err != nil {
		return processor.ChargeResult{}, err
	}
	return processor.ChargeResult{
		ProcessorChargeID: transfer.GetId(),
		Status:            chargeStatus(transfer.GetStatus()),
	}, nil
}

// RefundCharge is not supported for ACH transfers in this iteration: Plaid
// reversals use a separate refund API not yet modeled here.
func (c *Client) RefundCharge(_ context.Context, _, _ string, _ int64, _ string) (processor.RefundResult, error) {
	return processor.RefundResult{}, processor.NewError(processor.CodeInvalidRequest, false, nil,
		"Plaid ACH refunds are not supported yet")
}

// CreatePayout credits a merchant bank account via an ACH transfer.
func (c *Client) CreatePayout(ctx context.Context, req processor.PayoutRequest) (processor.PayoutResult, error) {
	transfer, err := c.transfer(ctx, req.Mode, plaid.TRANSFERTYPE_CREDIT, req.ProcessorAcctID, req.Amount, "payout")
	if err != nil {
		return processor.PayoutResult{}, err
	}
	return processor.PayoutResult{
		ProcessorPayoutID: transfer.GetId(),
		Status:            payoutStatus(transfer.GetStatus()),
	}, nil
}

// transfer runs the authorize-then-create flow shared by debit and credit
// transfers, with retry on transient failures.
func (c *Client) transfer(ctx context.Context, mode string, kind plaid.TransferType, tokenID string, amount int64, description string) (plaid.Transfer, error) {
	api, err := c.forMode(mode)
	if err != nil {
		return plaid.Transfer{}, err
	}
	accessToken, accountID, err := splitToken(tokenID)
	if err != nil {
		return plaid.Transfer{}, err
	}
	amountStr := formatAmount(amount)

	// Step 1 — authorize. Plaid decides whether to permit the transfer.
	user := plaid.NewTransferAuthorizationUserInRequest(transferUserLegalName)
	authReq := plaid.NewTransferAuthorizationCreateRequest(accessToken, accountID, kind, plaid.TRANSFERNETWORK_ACH, amountStr, *user)
	authResp, err := processor.Retry(ctx, c.policy, func() (plaid.TransferAuthorizationCreateResponse, error) {
		resp, httpResp, callErr := api.PlaidApi.TransferAuthorizationCreate(ctx).TransferAuthorizationCreateRequest(*authReq).Execute()
		return resp, classify(httpResp, callErr)
	})
	if err != nil {
		return plaid.Transfer{}, err
	}
	authorization := authResp.GetAuthorization()

	// Step 2 — create the transfer against the granted authorization.
	createReq := plaid.NewTransferCreateRequest(accessToken, accountID, authorization.GetId(), description)
	createResp, err := processor.Retry(ctx, c.policy, func() (plaid.TransferCreateResponse, error) {
		resp, httpResp, callErr := api.PlaidApi.TransferCreate(ctx).TransferCreateRequest(*createReq).Execute()
		return resp, classify(httpResp, callErr)
	})
	if err != nil {
		return plaid.Transfer{}, err
	}
	return createResp.GetTransfer(), nil
}

// CancelTransfer cancels a pending transfer by id.
func (c *Client) CancelTransfer(ctx context.Context, mode, transferID string) error {
	api, err := c.forMode(mode)
	if err != nil {
		return err
	}
	cancelReq := plaid.NewTransferCancelRequest(transferID)
	_, err = processor.Retry(ctx, c.policy, func() (plaid.TransferCancelResponse, error) {
		resp, httpResp, callErr := api.PlaidApi.TransferCancel(ctx).TransferCancelRequest(*cancelReq).Execute()
		return resp, classify(httpResp, callErr)
	})
	return err
}

// chargeStatus maps a Plaid transfer status to our succeeded/failed/pending model.
func chargeStatus(s plaid.TransferStatus) string {
	switch s {
	case "settled", "funds_available":
		return "succeeded"
	case "failed", "returned", "cancelled":
		return "failed"
	default: // pending / posted
		return "pending"
	}
}

// payoutStatus maps a Plaid transfer status to our payout status model.
func payoutStatus(s plaid.TransferStatus) string {
	switch s {
	case "settled", "funds_available":
		return "paid"
	case "failed", "returned", "cancelled":
		return "failed"
	default:
		return "pending"
	}
}

// splitToken parses an "accessToken|accountId" token id into its parts.
func splitToken(tokenID string) (accessToken, accountID string, err error) {
	parts := strings.SplitN(tokenID, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", processor.NewError(processor.CodeInvalidRequest, false, nil,
			"Plaid token must be formatted as accessToken|accountId")
	}
	return parts[0], parts[1], nil
}

// formatAmount renders integer minor units as Plaid's decimal-string amount,
// e.g. 1099 => "10.99".
func formatAmount(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// classify normalizes a Plaid SDK error using the HTTP status: 429 and 5xx are
// transient (retryable), everything else is a permanent request error.
func classify(httpResp *http.Response, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	if httpResp != nil {
		status = httpResp.StatusCode
	}
	switch {
	case status == http.StatusTooManyRequests:
		return processor.NewError(processor.CodeRateLimited, true, err, "plaid rate limited")
	case status >= 500:
		return processor.NewError(processor.CodeUnavailable, true, err, "plaid server error")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return processor.NewError(processor.CodeAuth, false, err, "plaid authentication failed")
	default:
		return processor.NewError(processor.CodeInvalidRequest, false, err, "plaid rejected request")
	}
}
