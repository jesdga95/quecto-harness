package llm

import "testing"

func TestPoolReusesAClient(t *testing.T) {
	p := NewPool()
	a := p.Get("http://one.example", "k")
	b := p.Get("http://one.example", "k")
	if a != b {
		t.Fatal("the same address and key gave two clients, so the protocol memo is thrown away every call")
	}
	if c := p.Get("http://one.example/", "k"); c != a {
		t.Error("a trailing slash gave a different client")
	}
}

func TestPoolSeparatesAddressesAndKeys(t *testing.T) {
	p := NewPool()
	base := p.Get("http://one.example", "k")
	if p.Get("http://two.example", "k") == base {
		t.Error("two addresses shared a client")
	}
	if p.Get("http://one.example", "other") == base {
		t.Error("two keys shared a client")
	}
}

func TestPoolRetainDrops(t *testing.T) {
	p := NewPool()
	kept := p.Get("http://one.example", "")
	p.Get("http://two.example", "")
	p.Retain([]string{Key("http://one.example", "")})

	p.mu.Lock()
	n := len(p.clients)
	p.mu.Unlock()
	if n != 1 {
		t.Fatalf("clients = %d, want 1", n)
	}
	if p.Get("http://one.example", "") != kept {
		t.Error("Retain dropped the client it was told to keep")
	}
}
