package llm

import (
	"strings"
	"sync"
)

// Pool hands out one client per address and key. A client memoises which
// protocol answered, so a run of requests to one address costs one probe a
// minute rather than one a request.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func NewPool() *Pool {
	return &Pool{clients: map[string]*Client{}}
}

// Key is the identity of a client. A changed address or a changed key is a
// different client, so nothing carries over from the one it replaces, and an
// edit that moves neither keeps the protocol the address settled on.
func Key(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "\x00" + token
}

func (p *Pool) Get(baseURL, token string) *Client {
	k := Key(baseURL, token)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[k]; ok {
		return c
	}
	c := New(baseURL, token)
	p.clients[k] = c
	return c
}

// Retain drops every client whose key is not in live, closing the connections
// it was holding. Called after a write that changed the set of addresses.
func (p *Pool) Retain(live []string) {
	keep := make(map[string]bool, len(live))
	for _, k := range live {
		keep[k] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.clients {
		if !keep[k] {
			delete(p.clients, k)
			c.CloseIdle()
		}
	}
}
