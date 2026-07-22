package outbox

import (
	"context"
	"testing"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"google.golang.org/protobuf/proto"
)

func TestNewProtoMessageEnvelope(t *testing.T) {
	payload := &commonv1.Money{MinorUnits: 500, Currency: "KZT"}
	m, err := NewProtoMessage(context.Background(), "ledger.transactions", "acct-1", "req-42", payload)
	if err != nil {
		t.Fatal(err)
	}
	if m.Topic != "ledger.transactions" || m.Key != "acct-1" || m.ID == "" {
		t.Fatalf("message fields: %+v", m)
	}
	var env eventsv1.EventEnvelope
	if err := proto.Unmarshal(m.Payload, &env); err != nil {
		t.Fatal(err)
	}
	if env.GetEventId() != m.ID || env.GetRequestId() != "req-42" || env.GetOccurredAt() == nil {
		t.Fatalf("envelope: %+v", &env)
	}
	var got commonv1.Money
	if err := env.GetPayload().UnmarshalTo(&got); err != nil {
		t.Fatal(err)
	}
	if got.GetMinorUnits() != 500 {
		t.Fatalf("payload round trip: %d", got.GetMinorUnits())
	}
	// event ids are unique per call (UUIDv7).
	m2, _ := NewProtoMessage(context.Background(), "t", "k", "r", payload)
	if m2.ID == m.ID {
		t.Fatal("event ids must be unique")
	}
}
