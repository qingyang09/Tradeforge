package execution

import "testing"

func TestNewBrokerPaperNeedsNoCredentials(t *testing.T) {
	b, err := NewBroker(BrokerKindPaper, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*PaperBroker); !ok {
		t.Errorf("paper should give a pure in-memory paper broker, got: %T", b)
	}
}

func TestNewBrokerBinanceTestnetRequiresCredentials(t *testing.T) {
	if _, err := NewBroker(BrokerKindBinanceTestnet, "", "", ""); err == nil {
		t.Fatal("missing credentials should produce an error")
	}
	b, err := NewBroker(BrokerKindBinanceTestnet, "k", "s", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name() != "binance-testnet" {
		t.Errorf("Name() = %q, want binance-testnet", b.Name())
	}
}

func TestNewBrokerOKXDemoRequiresPassphrase(t *testing.T) {
	if _, err := NewBroker(BrokerKindOKXDemo, "k", "s", ""); err == nil {
		t.Fatal("OKX missing a passphrase should produce an error")
	}
	b, err := NewBroker(BrokerKindOKXDemo, "k", "s", "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q, want okx-demo", b.Name())
	}
}

func TestNewBrokerBybitTestnetRequiresCredentials(t *testing.T) {
	if _, err := NewBroker(BrokerKindBybitTestnet, "", "", ""); err == nil {
		t.Fatal("missing credentials should produce an error")
	}
	b, err := NewBroker(BrokerKindBybitTestnet, "k", "s", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name() != "bybit-testnet" {
		t.Errorf("Name() = %q, want bybit-testnet", b.Name())
	}
}

func TestNewBrokerBitgetDemoRequiresPassphrase(t *testing.T) {
	if _, err := NewBroker(BrokerKindBitgetDemo, "k", "s", ""); err == nil {
		t.Fatal("Bitget missing a passphrase should produce an error")
	}
	b, err := NewBroker(BrokerKindBitgetDemo, "k", "s", "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name() != "bitget-demo" {
		t.Errorf("Name() = %q, want bitget-demo", b.Name())
	}
}

func TestNewBrokerRejectsUnknownKind(t *testing.T) {
	if _, err := NewBroker(BrokerKind("coinbase-sandbox"), "k", "s", ""); err == nil {
		t.Fatal("an unregistered order channel should produce an error")
	}
}

func TestBrokerKindRequiresPassphrase(t *testing.T) {
	if BrokerKindBinanceTestnet.RequiresPassphrase() {
		t.Error("Binance should not need a passphrase")
	}
	if !BrokerKindOKXDemo.RequiresPassphrase() {
		t.Error("OKX should need a passphrase")
	}
	if BrokerKindBybitTestnet.RequiresPassphrase() {
		t.Error("Bybit should not need a passphrase")
	}
	if !BrokerKindBitgetDemo.RequiresPassphrase() {
		t.Error("Bitget should need a passphrase")
	}
}

func TestCredentialedKindsExcludesPaper(t *testing.T) {
	for _, k := range CredentialedKinds {
		if k == BrokerKindPaper {
			t.Error("CredentialedKinds should not include paper — it has no key/secret to fill in")
		}
	}
}
