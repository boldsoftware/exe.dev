package exeprox

import (
	"context"

	"github.com/go4org/hashtriemap"
)

// exe1TokensData holds the exe1 token cache, mapped by exe1 token string.
type exe1TokensData struct {
	tokens hashtriemap.HashTrieMap[string, string] // exe1 → exe0
}

// lookup returns the exe0 token for an exe1 token.
// The bool result reports whether the token exists.
func (d *exe1TokensData) lookup(ctx context.Context, exeproxData ExeproxData, exe1Token string) (exe0Token string, exists bool, err error) {
	val, ok := d.tokens.Load(exe1Token)
	if ok {
		return val, true, nil
	}
	val, exists, err = exeproxData.ResolveExe1Token(ctx, exe1Token)
	if err == nil && exists {
		d.tokens.Store(exe1Token, val)
	}
	return val, exists, err
}

// clear clears the exe1 tokens cache.
func (d *exe1TokensData) clear() {
	d.tokens.Clear()
}
