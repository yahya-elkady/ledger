package payments

import (
	"errors"
	"fmt"
)

// AccountType classifies an account in the chart of accounts.
// It determines normal balance behaviour: asset and expense accounts
// increase with debits; liability, equity, and revenue accounts
// increase with credits.
type AccountType string

const (
	AccountTypeAsset     AccountType = "asset"
	AccountTypeLiability AccountType = "liability"
	AccountTypeEquity    AccountType = "equity"
	AccountTypeRevenue   AccountType = "revenue"
	AccountTypeExpense   AccountType = "expense"
)

// validAccountTypes is the set of recognised AccountType values.
var validAccountTypes = map[AccountType]bool{
	AccountTypeAsset:     true,
	AccountTypeLiability: true,
	AccountTypeEquity:    true,
	AccountTypeRevenue:   true,
	AccountTypeExpense:   true,
}

// NormalBalance returns the Direction that increases this account type's balance.
// Debits increase assets and expenses; credits increase liabilities, equity, revenue.
func (t AccountType) NormalBalance() Direction {
	switch t {
	case AccountTypeAsset, AccountTypeExpense:
		return Debit
	default:
		return Credit
	}
}

// Account represents a single account in the double-entry ledger's chart of accounts.
//
// System accounts (e.g. a "cash float" account, a "fees collected" account)
// are modelled here alongside user-facing accounts — the Type field distinguishes them.
type Account struct {
	ID       string
	Name     string
	Currency string      // ISO 4217; entries against this account must use the same currency
	Type     AccountType
}

// Validate returns an error if the account is missing required fields or has
// an unrecognised type.
func (a Account) Validate() error {
	var errs []error
	if a.ID == "" {
		errs = append(errs, errors.New("account ID is required"))
	}
	if a.Name == "" {
		errs = append(errs, errors.New("account name is required"))
	}
	if a.Currency == "" {
		errs = append(errs, errors.New("account currency is required"))
	}
	if !validAccountTypes[a.Type] {
		errs = append(errs, fmt.Errorf("unknown account type %q", a.Type))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}