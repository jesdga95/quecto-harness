package search

import (
	"strings"
	"testing"
)

func TestNamesListEveryProvider(t *testing.T) {
	if len(Names) == 0 || Names[0] != NameExa {
		t.Errorf("Names = %v, want exa first", Names)
	}
	if !ValidName("exa") || ValidName("nope") || ValidName("") {
		t.Error("ValidName disagrees with Names")
	}
}

func TestDefaultURLNamesTheEndpoint(t *testing.T) {
	if got := DefaultURL(NameExa); got != "https://api.exa.ai" {
		t.Errorf("DefaultURL(exa) = %q", got)
	}
	if got := DefaultURL(Name("nope")); got != "" {
		t.Errorf("DefaultURL(unknown) = %q, want nothing", got)
	}
}

func TestForBuildsTheNamedProvider(t *testing.T) {
	p := NewProviders()
	prov, err := p.For(NameExa, "https://example.org", "k")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if prov.Name() != NameExa {
		t.Errorf("Name = %q, want exa", prov.Name())
	}
	x, ok := prov.(*exa)
	if !ok {
		t.Fatalf("provider = %T", prov)
	}
	if x.http != p.HTTP {
		t.Error("the provider did not take the shared client")
	}
}

func TestForRefusesANameItDoesNotKnow(t *testing.T) {
	p := NewProviders()
	_, err := p.For(Name("nope"), "https://example.org", "k")
	if err == nil || !strings.Contains(err.Error(), "no provider named nope") {
		t.Errorf("err = %v, want the unknown name refused", err)
	}
}

func TestForTakesTheDefaultAddress(t *testing.T) {
	p := NewProviders()
	prov, err := p.For(NameExa, "", "k")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	x, ok := prov.(*exa)
	if !ok {
		t.Fatalf("provider = %T", prov)
	}
	if x.baseURL != "https://api.exa.ai" {
		t.Errorf("baseURL = %q, want the provider's default", x.baseURL)
	}
}
