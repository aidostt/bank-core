package http

import (
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
)

// Money in JSON: integer minor units + ISO currency, never floats.
type MoneyDTO struct {
	MinorUnits int64  `json:"minor_units"`
	Currency   string `json:"currency"`
}

type UserDTO struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone,omitempty"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"created_at"`
}

func userDTO(u *identityv1.User) UserDTO {
	return UserDTO{
		ID: u.GetId(), Email: u.GetEmail(), Name: u.GetName(), Phone: u.GetPhone(),
		Roles: u.GetRoles(), CreatedAt: u.GetCreatedAt().AsTime(),
	}
}

type AccountDTO struct {
	ID       string    `json:"id"`
	Number   string    `json:"number"`
	Currency string    `json:"currency"`
	Status   string    `json:"status"`
	OpenedAt time.Time `json:"opened_at"`
}

type BalanceDTO struct {
	Balance   int64     `json:"balance"`
	Held      int64     `json:"held"`
	Available int64     `json:"available"`
	AsOf      time.Time `json:"as_of"`
}

type AccountWithBalanceDTO struct {
	AccountDTO
	Balance *BalanceDTO `json:"balance,omitempty"`
}

func accountDTO(a *accountv1.Account) AccountDTO {
	return AccountDTO{
		ID: a.GetId(), Number: a.GetNumber(), Currency: a.GetCurrency(),
		Status: a.GetStatus(), OpenedAt: a.GetOpenedAt().AsTime(),
	}
}

func accountWithBalanceDTO(awb *accountv1.AccountWithBalance) AccountWithBalanceDTO {
	out := AccountWithBalanceDTO{AccountDTO: accountDTO(awb.GetAccount())}
	if b := awb.GetBalance(); b != nil {
		out.Balance = &BalanceDTO{
			Balance: b.GetBalance(), Held: b.GetHeld(),
			Available: b.GetAvailable(), AsOf: b.GetAsOf().AsTime(),
		}
	}
	return out
}

type TransferDTO struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	State         string    `json:"state"`
	FromAccountID string    `json:"from_account_id,omitempty"`
	ToAccountID   string    `json:"to_account_id,omitempty"`
	Amount        MoneyDTO  `json:"amount"`
	CounterAmount *MoneyDTO `json:"counter_amount,omitempty"`
	AppliedRate   string    `json:"applied_rate,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func transferDTO(t *transferv1.TransferView) TransferDTO {
	out := TransferDTO{
		ID:            t.GetId(),
		Type:          trimEnumPrefix(t.GetType().String(), "TRANSFER_TYPE_"),
		State:         trimEnumPrefix(t.GetState().String(), "TRANSFER_STATE_"),
		FromAccountID: t.GetFromAccountId(),
		ToAccountID:   t.GetToAccountId(),
		Amount:        MoneyDTO{MinorUnits: t.GetAmount().GetMinorUnits(), Currency: t.GetAmount().GetCurrency()},
		AppliedRate:   t.GetAppliedRate(),
		Reason:        t.GetReason(),
		CreatedAt:     t.GetCreatedAt().AsTime(),
		UpdatedAt:     t.GetUpdatedAt().AsTime(),
	}
	if ca := t.GetCounterAmount(); ca != nil && ca.GetCurrency() != "" {
		out.CounterAmount = &MoneyDTO{MinorUnits: ca.GetMinorUnits(), Currency: ca.GetCurrency()}
	}
	return out
}

func trimEnumPrefix(s, prefix string) string {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
