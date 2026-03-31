package auth

import "context"

// ConnectionCheckerAdapter combines an OAuthResolver (upstream→provider mapping + token lookup)
// and an OAuthHandler (signed URL generation) to implement proxy.ConnectionChecker.
type ConnectionCheckerAdapter struct {
	resolver *OAuthResolver
	handler  *OAuthHandler
}

// NewConnectionChecker creates a ConnectionCheckerAdapter.
func NewConnectionChecker(resolver *OAuthResolver, handler *OAuthHandler) *ConnectionCheckerAdapter {
	return &ConnectionCheckerAdapter{resolver: resolver, handler: handler}
}

// IsConnected reports whether the caller has a token for the given upstream.
func (c *ConnectionCheckerAdapter) IsConnected(ctx context.Context, callerName, upstreamName string) (bool, string) {
	return c.resolver.IsConnected(ctx, callerName, upstreamName)
}

// ConnectURL returns a signed URL the user can open in a browser to connect.
func (c *ConnectionCheckerAdapter) ConnectURL(callerName, provider string) string {
	return c.handler.GenerateConnectURL(callerName, provider)
}
