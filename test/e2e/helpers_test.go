//go:build e2e

package e2e

import (
	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/testutil"
)

func adminOf(*testutil.Server) service.Principal {
	return service.Principal{KeyID: "e2e", Name: "e2e", Scope: api.ScopeAdmin}
}
