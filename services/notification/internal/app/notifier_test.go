package app

import "testing"

// Template snapshot tests (notification doc DoD): exact rendered strings.
func TestTemplateSnapshots(t *testing.T) {
	cases := []struct {
		name     string
		template string
		data     RenderData
		want     string
	}{
		{
			"p2p completed same currency",
			"transfer_completed",
			RenderData{Type: "TRANSFER_TYPE_P2P", Amount: "15000.00 KZT"},
			"Your TRANSFER_TYPE_P2P transfer of 15000.00 KZT is complete.",
		},
		{
			"fx completed with counter amount",
			"transfer_completed",
			RenderData{Type: "TRANSFER_TYPE_INTERNAL", Amount: "200.00 USD", Counter: "95650.00 KZT"},
			"Your TRANSFER_TYPE_INTERNAL transfer of 200.00 USD is complete — the beneficiary received 95650.00 KZT.",
		},
		{
			"failed with reason",
			"transfer_failed",
			RenderData{Type: "TRANSFER_TYPE_P2P", Amount: "10.00 USD", Reason: "INSUFFICIENT_FUNDS"},
			"Your TRANSFER_TYPE_P2P transfer of 10.00 USD failed: INSUFFICIENT_FUNDS.",
		},
		{
			"high fraud alert freezes",
			"fraud_alert",
			RenderData{Rule: "R2", Severity: "HIGH", High: true},
			"Suspicious activity detected on your account (rule R2, severity HIGH). The affected account has been frozen — contact support.",
		},
		{
			"medium fraud alert informs",
			"fraud_alert",
			RenderData{Rule: "R1", Severity: "MEDIUM"},
			"Suspicious activity detected on your account (rule R1, severity MEDIUM). No action is required; we are monitoring.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Render(c.template, c.data)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("rendered:\n%q\nwant:\n%q", got, c.want)
			}
		})
	}
}

func TestFormatMoney(t *testing.T) {
	if s := formatMoney(1500000, "KZT"); s != "15000.00 KZT" {
		t.Fatal(s)
	}
	if s := formatMoney(5, "USD"); s != "0.05 USD" {
		t.Fatal(s)
	}
	if s := formatMoney(-4782, "KZT"); s != "-47.82 KZT" {
		t.Fatal(s)
	}
}
