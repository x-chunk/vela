package server

import (
	"context"

	"go.xchunk.org/vela/internal/service"
)

type principalKey struct{}

func withPrincipal(ctx context.Context, p service.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalFrom(ctx context.Context) service.Principal {
	p, _ := ctx.Value(principalKey{}).(service.Principal)
	return p
}
