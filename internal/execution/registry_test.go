package execution

import "testing"

func TestNewBrokerPaperNeedsNoCredentials(t *testing.T) {
	b, err := NewBroker(BrokerKindPaper, "", "", "")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if _, ok := b.(*PaperBroker); !ok {
		t.Errorf("paper 应该给纯内存模拟盘，实际：%T", b)
	}
}

func TestNewBrokerBinanceTestnetRequiresCredentials(t *testing.T) {
	if _, err := NewBroker(BrokerKindBinanceTestnet, "", "", ""); err == nil {
		t.Fatal("缺少凭据时应该报错")
	}
	b, err := NewBroker(BrokerKindBinanceTestnet, "k", "s", "")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if b.Name() != "binance-testnet" {
		t.Errorf("Name() = %q，期望 binance-testnet", b.Name())
	}
}

func TestNewBrokerOKXDemoRequiresPassphrase(t *testing.T) {
	if _, err := NewBroker(BrokerKindOKXDemo, "k", "s", ""); err == nil {
		t.Fatal("OKX 缺少 passphrase 时应该报错")
	}
	b, err := NewBroker(BrokerKindOKXDemo, "k", "s", "p")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q，期望 okx-demo", b.Name())
	}
}

func TestNewBrokerBybitTestnetRequiresCredentials(t *testing.T) {
	if _, err := NewBroker(BrokerKindBybitTestnet, "", "", ""); err == nil {
		t.Fatal("缺少凭据时应该报错")
	}
	b, err := NewBroker(BrokerKindBybitTestnet, "k", "s", "")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if b.Name() != "bybit-testnet" {
		t.Errorf("Name() = %q，期望 bybit-testnet", b.Name())
	}
}

func TestNewBrokerBitgetDemoRequiresPassphrase(t *testing.T) {
	if _, err := NewBroker(BrokerKindBitgetDemo, "k", "s", ""); err == nil {
		t.Fatal("Bitget 缺少 passphrase 时应该报错")
	}
	b, err := NewBroker(BrokerKindBitgetDemo, "k", "s", "p")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if b.Name() != "bitget-demo" {
		t.Errorf("Name() = %q，期望 bitget-demo", b.Name())
	}
}

func TestNewBrokerRejectsUnknownKind(t *testing.T) {
	if _, err := NewBroker(BrokerKind("coinbase-sandbox"), "k", "s", ""); err == nil {
		t.Fatal("未注册的下单通道应该报错")
	}
}

func TestBrokerKindRequiresPassphrase(t *testing.T) {
	if BrokerKindBinanceTestnet.RequiresPassphrase() {
		t.Error("币安不需要 passphrase")
	}
	if !BrokerKindOKXDemo.RequiresPassphrase() {
		t.Error("OKX 需要 passphrase")
	}
	if BrokerKindBybitTestnet.RequiresPassphrase() {
		t.Error("Bybit 不需要 passphrase")
	}
	if !BrokerKindBitgetDemo.RequiresPassphrase() {
		t.Error("Bitget 需要 passphrase")
	}
}

func TestCredentialedKindsExcludesPaper(t *testing.T) {
	for _, k := range CredentialedKinds {
		if k == BrokerKindPaper {
			t.Error("CredentialedKinds 不该包含 paper——它没有 key/secret 可填")
		}
	}
}
