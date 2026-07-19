package http

import (
	"net/http"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		writeProblem(c, apperr.Wrap(apperr.CodeInvalidArgument, "malformed request body", err))
		return false
	}
	return true
}

// --- auth ---

func (s *Server) handleRegister(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
		Phone    string `json:"phone"`
	}
	if !bindJSON(c, &req) {
		return
	}
	resp, err := s.clients.Identity.Register(c.Request.Context(), &identityv1.RegisterRequest{
		Email: req.Email, Password: req.Password, Name: req.Name, Phone: req.Phone,
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusCreated, userDTO(resp.GetUser()))
}

func (s *Server) handleLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	resp, err := s.clients.Identity.Login(c.Request.Context(), &identityv1.LoginRequest{
		Email: req.Email, Password: req.Password,
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token":  resp.GetAccessToken(),
		"refresh_token": resp.GetRefreshToken(),
		"expires_in":    resp.GetExpiresInSeconds(),
		"user":          userDTO(resp.GetUser()),
	})
}

func (s *Server) handleRefresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !bindJSON(c, &req) {
		return
	}
	resp, err := s.clients.Identity.Refresh(c.Request.Context(), &identityv1.RefreshRequest{RefreshToken: req.RefreshToken})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token":  resp.GetAccessToken(),
		"refresh_token": resp.GetRefreshToken(),
		"expires_in":    resp.GetExpiresInSeconds(),
	})
}

func (s *Server) handleLogout(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if _, err := s.clients.Identity.Logout(c.Request.Context(), &identityv1.LogoutRequest{RefreshToken: req.RefreshToken}); err != nil {
		writeProblem(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) handleGetMe(c *gin.Context) {
	resp, err := s.clients.Identity.GetMe(c.Request.Context(), &identityv1.GetMeRequest{})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, userDTO(resp.GetUser()))
}

// --- accounts ---

func (s *Server) handleOpenAccount(c *gin.Context) {
	var req struct {
		Currency string `json:"currency"`
	}
	if !bindJSON(c, &req) {
		return
	}
	resp, err := s.clients.Account.OpenAccount(c.Request.Context(), &accountv1.OpenAccountRequest{Currency: req.Currency})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusCreated, accountDTO(resp.GetAccount()))
}

func (s *Server) handleListAccounts(c *gin.Context) {
	resp, err := s.clients.Account.ListAccountsByCustomer(c.Request.Context(), &accountv1.ListAccountsByCustomerRequest{})
	if err != nil {
		writeProblem(c, err)
		return
	}
	out := make([]AccountWithBalanceDTO, 0, len(resp.GetAccounts()))
	for _, a := range resp.GetAccounts() {
		out = append(out, accountWithBalanceDTO(a))
	}
	c.JSON(http.StatusOK, gin.H{"accounts": out})
}

func (s *Server) handleListTransactions(c *gin.Context) {
	from := time.Now().UTC().AddDate(0, -1, 0)
	to := time.Now().UTC().Add(time.Minute)
	if v := c.Query("from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(c, apperr.New(apperr.CodeInvalidArgument, "from must be RFC3339"))
			return
		}
		from = parsed
	}
	if v := c.Query("to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(c, apperr.New(apperr.CodeInvalidArgument, "to must be RFC3339"))
			return
		}
		to = parsed
	}
	resp, err := s.clients.Account.ListTransactions(c.Request.Context(), &accountv1.ListTransactionsRequest{
		AccountId: c.Param("id"),
		From:      timestamppb.New(from),
		To:        timestamppb.New(to),
		PageSize:  int32(min(parseIntDefault(c.Query("page_size"), 50), 200)), // #nosec G115 -- bounded above
		Cursor:    c.Query("cursor"),
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	type txDTO struct {
		EntryID    string    `json:"entry_id"`
		Amount     MoneyDTO  `json:"amount"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	out := make([]txDTO, 0, len(resp.GetTransactions()))
	for _, t := range resp.GetTransactions() {
		out = append(out, txDTO{
			EntryID:    t.GetEntryId(),
			Amount:     MoneyDTO{MinorUnits: t.GetAmount(), Currency: t.GetCurrency()},
			OccurredAt: t.GetOccurredAt().AsTime(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"transactions": out, "next_cursor": resp.GetNextCursor()})
}

// --- transfers ---

func (s *Server) handleTopup(c *gin.Context) {
	var req struct {
		AccountID  string `json:"account_id"`
		MinorUnits int64  `json:"minor_units"`
		Currency   string `json:"currency"`
	}
	if !bindJSON(c, &req) {
		return
	}
	s.createTransfer(c, &transferv1.CreateTransferRequest{
		Type:        transferv1.TransferType_TRANSFER_TYPE_TOPUP,
		ToAccountId: req.AccountID,
		Amount:      &commonv1.Money{MinorUnits: req.MinorUnits, Currency: req.Currency},
	})
}

func (s *Server) handleCreateTransfer(c *gin.Context) {
	var req struct {
		Type            string `json:"type"` // INTERNAL | P2P
		FromAccountID   string `json:"from_account_id"`
		ToAccountID     string `json:"to_account_id"`
		ToAccountNumber string `json:"to_account_number"`
		MinorUnits      int64  `json:"minor_units"`
		Currency        string `json:"currency"`
	}
	if !bindJSON(c, &req) {
		return
	}
	var t transferv1.TransferType
	switch req.Type {
	case "INTERNAL":
		t = transferv1.TransferType_TRANSFER_TYPE_INTERNAL
	case "P2P":
		t = transferv1.TransferType_TRANSFER_TYPE_P2P
	default:
		writeProblem(c, apperr.New(apperr.CodeInvalidArgument, `type must be "INTERNAL" or "P2P"`))
		return
	}
	s.createTransfer(c, &transferv1.CreateTransferRequest{
		Type:            t,
		FromAccountId:   req.FromAccountID,
		ToAccountId:     req.ToAccountID,
		ToAccountNumber: req.ToAccountNumber,
		Amount:          &commonv1.Money{MinorUnits: req.MinorUnits, Currency: req.Currency},
	})
}

func (s *Server) createTransfer(c *gin.Context, req *transferv1.CreateTransferRequest) {
	resp, err := s.clients.Transfer.CreateTransfer(c.Request.Context(), req)
	if err != nil {
		writeProblem(c, err)
		return
	}
	view := resp.GetTransfer()
	c.Header("Location", "/v1/transfers/"+view.GetId())
	// Terminal → 201; ambiguous/in-flight → 202 and the client polls
	// GetTransfer (honest distributed-systems UX — transfer doc).
	status := http.StatusCreated
	switch view.GetState() {
	case transferv1.TransferState_TRANSFER_STATE_COMPLETED, transferv1.TransferState_TRANSFER_STATE_FAILED:
		status = http.StatusCreated
	default:
		status = http.StatusAccepted
	}
	c.JSON(status, transferDTO(view))
}

func (s *Server) handleGetTransfer(c *gin.Context) {
	resp, err := s.clients.Transfer.GetTransfer(c.Request.Context(), &transferv1.GetTransferRequest{TransferId: c.Param("id")})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, transferDTO(resp.GetTransfer()))
}

func (s *Server) handleListTransfers(c *gin.Context) {
	resp, err := s.clients.Transfer.ListTransfers(c.Request.Context(), &transferv1.ListTransfersRequest{
		PageSize: int32(min(parseIntDefault(c.Query("page_size"), 20), 100)), // #nosec G115 -- bounded above
		Cursor:   c.Query("cursor"),
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	out := make([]TransferDTO, 0, len(resp.GetTransfers()))
	for _, t := range resp.GetTransfers() {
		out = append(out, transferDTO(t))
	}
	c.JSON(http.StatusOK, gin.H{"transfers": out, "next_cursor": resp.GetNextCursor()})
}

func (s *Server) handleGetRates(c *gin.Context) {
	resp, err := s.clients.Transfer.GetRates(c.Request.Context(), &transferv1.GetRatesRequest{})
	if err != nil {
		writeProblem(c, err)
		return
	}
	type rateDTO struct {
		Pair      string    `json:"pair"`
		Buy       string    `json:"buy"`
		Sell      string    `json:"sell"`
		ValidFrom time.Time `json:"valid_from"`
	}
	out := make([]rateDTO, 0, len(resp.GetRates()))
	for _, r := range resp.GetRates() {
		out = append(out, rateDTO{Pair: r.GetPair(), Buy: r.GetBuy(), Sell: r.GetSell(), ValidFrom: r.GetValidFrom().AsTime()})
	}
	c.JSON(http.StatusOK, gin.H{"rates": out})
}

// --- admin ---

func (s *Server) handleAdminListAccounts(c *gin.Context) {
	resp, err := s.clients.Account.ListAccountsByCustomer(c.Request.Context(), &accountv1.ListAccountsByCustomerRequest{
		UserId: c.Param("id"),
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	out := make([]AccountWithBalanceDTO, 0, len(resp.GetAccounts()))
	for _, a := range resp.GetAccounts() {
		out = append(out, accountWithBalanceDTO(a))
	}
	c.JSON(http.StatusOK, gin.H{"accounts": out})
}

func (s *Server) handleFreeze(c *gin.Context) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req) // body optional
	resp, err := s.clients.Account.Freeze(c.Request.Context(), &accountv1.FreezeRequest{
		AccountId: c.Param("id"), Reason: req.Reason,
	})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, accountDTO(resp.GetAccount()))
}

func (s *Server) handleUnfreeze(c *gin.Context) {
	resp, err := s.clients.Account.Unfreeze(c.Request.Context(), &accountv1.UnfreezeRequest{AccountId: c.Param("id")})
	if err != nil {
		writeProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, accountDTO(resp.GetAccount()))
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
		if n > 1<<20 {
			return def
		}
	}
	return n
}
