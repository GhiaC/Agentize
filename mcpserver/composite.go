package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ghiac/agentize/mcpauth"
	"sort"
)

// Combine joins host capabilities and Agentize on one endpoint. Duplicate names
// fail closed instead of silently replacing a handler or its security policy.
func Combine(providers ...Provider) Provider { return combinedProvider(providers) }

type combinedProvider []Provider

func (p combinedProvider) catalog(ctx context.Context, principal mcpauth.Principal) ([]Tool, map[string]Provider, error) {
	tools := []Tool{}
	owners := map[string]Provider{}
	for _, provider := range p {
		if provider == nil {
			return nil, nil, errors.New("missing provider")
		}
		list, err := provider.List(ctx, principal)
		if err != nil {
			return nil, nil, err
		}
		for _, tool := range list {
			if _, ok := owners[tool.Name]; ok {
				return nil, nil, errors.New("ambiguous tool name")
			}
			owners[tool.Name] = provider
			tools = append(tools, tool)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, owners, nil
}
func (p combinedProvider) List(ctx context.Context, principal mcpauth.Principal) ([]Tool, error) {
	tools, _, err := p.catalog(ctx, principal)
	return tools, err
}
func (p combinedProvider) Call(ctx context.Context, principal mcpauth.Principal, name string, args json.RawMessage) (any, error) {
	_, owners, err := p.catalog(ctx, principal)
	if err != nil {
		return nil, err
	}
	provider := owners[name]
	if provider == nil {
		return nil, errors.New("unavailable tool")
	}
	return provider.Call(ctx, principal, name, args)
}
