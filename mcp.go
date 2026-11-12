package agentize

import (
	"github.com/ghiac/agentize/mcpauth"
	"github.com/ghiac/agentize/mcpserver"
	"net/http"
)

// MCPHandler exposes this Agentize's registered capabilities, conversations and
// files on one HTTP endpoint. Serve it on the host's existing authenticated TLS
// listener. Credentials are independently verified for every request.
func (ag *Agentize) MCPHandler(access *mcpauth.Service, ledger mcpauth.UsageStore) http.Handler {
	if ag == nil || ag.GetEngine() == nil {
		return mcpserver.Handler(access, nil, ledger)
	}
	server, err := mcpserver.NewProvider(ag.GetEngine().MCPProvider())
	if err != nil {
		return mcpserver.Handler(access, nil, ledger)
	}
	return mcpserver.Handler(access, server, ledger)
}
