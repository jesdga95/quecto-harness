package llm

import "testing"

func TestPoolReusesAClient(t *testing.T) {
	p := NewPool()
	a := p.Get("http://one.example", "k", ProtocolLlama)
	b := p.Get("http://one.example", "k", ProtocolLlama)
	if a != b {
		t.Fatal("the same address, key and protocol gave two clients")
	}
	if c := p.Get("http://one.example/", "k", ProtocolLlama); c != a {
		t.Error("a trailing slash gave a different client")
	}
}

func TestPoolSeparatesAddressesAndKeys(t *testing.T) {
	p := NewPool()
	base := p.Get("http://one.example", "k", ProtocolLlama)
	if p.Get("http://two.example", "k", ProtocolLlama) == base {
		t.Error("two addresses shared a client")
	}
	if p.Get("http://one.example", "other", ProtocolLlama) == base {
		t.Error("two keys shared a client")
	}
	if p.Get("http://one.example", "k", ProtocolChat) == base {
		t.Error("two protocols shared a client, so one address could only be spoken to one way")
	}
}

func TestPoolRetainDrops(t *testing.T) {
	p := NewPool()
	kept := p.Get("http://one.example", "", ProtocolLlama)
	p.Get("http://two.example", "", ProtocolLlama)
	p.Retain([]string{Key("http://one.example", "", ProtocolLlama)})

	p.mu.Lock()
	n := len(p.clients)
	p.mu.Unlock()
	if n != 1 {
		t.Fatalf("clients = %d, want 1", n)
	}
	if p.Get("http://one.example", "", ProtocolLlama) != kept {
		t.Error("Retain dropped the client it was told to keep")
	}
}
