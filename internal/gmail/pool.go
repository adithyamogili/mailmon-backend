package gmail

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
)

type ClientPool struct {
	clients  map[string]*Client
	oauthCfg *oauth2.Config
	mu       sync.RWMutex
}

func NewPool(oauthCfg *oauth2.Config) *ClientPool {
	return &ClientPool{
		clients:  make(map[string]*Client),
		oauthCfg: oauthCfg,
	}
}

func (p *ClientPool) Get(userID string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[userID]
	return c, ok
}

func (p *ClientPool) AddFromToken(ctx context.Context, userID string, tok *oauth2.Token, onRefresh OnTokenRefresh) error {
	client, err := NewClientFromToken(ctx, p.oauthCfg, tok, onRefresh)
	if err != nil {
		return fmt.Errorf("gmail.Pool.AddFromToken: %w", err)
	}
	p.mu.Lock()
	p.clients[userID] = client
	p.mu.Unlock()
	return nil
}

func (p *ClientPool) Remove(userID string) {
	p.mu.Lock()
	delete(p.clients, userID)
	p.mu.Unlock()
}
