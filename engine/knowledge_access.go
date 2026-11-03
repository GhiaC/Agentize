package engine

import (
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
)

func (e *Engine) knowledgePermissions(userID, nodePath string) (*model.Node, *model.Permissions, error) {
	if e == nil || e.Repo == nil || strings.TrimSpace(userID) == "" {
		return nil, nil, fmt.Errorf("access denied: knowledge identity is unavailable")
	}
	canonical, err := e.Repo.CanonicalPath(nodePath)
	if err != nil {
		return nil, nil, err
	}
	var roles, groups []string
	if e.KnowledgeMemberships != nil {
		roles, groups = e.KnowledgeMemberships(userID)
	}
	var permissions *model.Permissions
	var node *model.Node
	parts := strings.Split(canonical, "/")
	for i := range parts {
		if i > 0 && permissions != nil && !permissions.HasPermission(model.PermExecute) {
			return nil, nil, fmt.Errorf("access denied: knowledge traversal")
		}
		node, err = e.Repo.LoadNodeFresh(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, nil, err
		}
		inherited := permissions
		// Resolve the complete ancestry, rather than dropping grandparents during inheritance.
		permissions = nil
		if p := node.Auth.Users[userID]; p != nil {
			permissions = p
		}
		if permissions == nil {
			for _, group := range groups {
				if p := node.Auth.Groups[group]; p != nil {
					permissions = p
					break
				}
			}
		}
		if permissions == nil {
			for _, role := range roles {
				if p := node.Auth.Roles[role]; p != nil {
					permissions = p
					break
				}
			}
		}
		if permissions == nil && node.Auth.Inherit && inherited != nil {
			permissions = inherited
		}
		if permissions == nil {
			permissions = node.Auth.Default
		}
		if permissions == nil && (!node.Auth.Inherit || len(node.Auth.Users) > 0 || len(node.Auth.Roles) > 0 || len(node.Auth.Groups) > 0) {
			permissions = &model.Permissions{}
		}
	}
	return node, permissions, nil
}

func (e *Engine) readableKnowledgeNode(userID, nodePath string) (*model.Node, error) {
	node, permissions, err := e.knowledgePermissions(userID, nodePath)
	if err != nil {
		return nil, err
	}
	if permissions != nil && !permissions.HasPermission(model.PermRead) {
		return nil, fmt.Errorf("access denied: knowledge read")
	}
	return node, nil
}

func (e *Engine) visibleKnowledgeNode(userID, nodePath string) (*model.Node, bool) {
	node, permissions, err := e.knowledgePermissions(userID, nodePath)
	if err != nil {
		return nil, false
	}
	return node, permissions == nil || permissions.HasPermission(model.PermSee)
}

func (e *Engine) executableKnowledgeNode(userID, nodePath string) (*model.Node, error) {
	node, permissions, err := e.knowledgePermissions(userID, nodePath)
	if err != nil {
		return nil, err
	}
	if permissions != nil && (!permissions.HasPermission(model.PermRead) || !permissions.HasPermission(model.PermExecute)) {
		return nil, fmt.Errorf("access denied: knowledge execution")
	}
	return node, nil
}

func knowledgePlatformTool(name string) bool {
	if platformKnowledgeToolName(name) {
		return true
	}
	switch name {
	case "manage_files", "manage_context", "collect_result", "inspect_result", "manage_schedules", "browser_use":
		return true
	}
	return false
}

// CheckKnowledgeToolAccess binds declared tools to a currently open, authorized node.
// Functions absent from the knowledge tree remain host-owned and keep their own guards.
func (e *Engine) CheckKnowledgeToolAccess(userID, sessionID, toolName string) error {
	if e == nil || e.Repo == nil || knowledgePlatformTool(toolName) {
		return nil
	}
	var owners []string
	var walk func(string) error
	walk = func(nodePath string) error {
		node, err := e.Repo.LoadNodeFresh(nodePath)
		if err != nil {
			return err
		}
		for _, tool := range node.Tools {
			e.knowledgeToolNames.Store(tool.Name, true)
			if tool.Name == toolName && tool.Status == model.ToolStatusActive {
				owners = append(owners, node.Path)
			}
		}
		children, err := e.Repo.GetChildren(nodePath)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("root"); err != nil {
		return fmt.Errorf("access denied: knowledge catalog is unavailable")
	}
	if len(owners) == 0 {
		if _, declared := e.knowledgeToolNames.Load(toolName); declared {
			return fmt.Errorf("access denied: knowledge tool is no longer active")
		}
		return nil
	}
	session, err := e.loadOwnedSession(userID, sessionID)
	if err != nil || session == nil {
		return fmt.Errorf("access denied: tool requires an owned session")
	}
	// Reload persisted activation: a caller-held session snapshot cannot reopen a closed node.
	for _, owner := range owners {
		if _, err := e.executableKnowledgeNode(userID, owner); err != nil {
			continue
		}
		for _, digest := range session.NodeDigests {
			canonical, err := e.Repo.CanonicalPath(digest.Path)
			if err == nil && canonical == owner {
				return nil
			}
		}
	}
	return fmt.Errorf("access denied: tool requires an open, authorized knowledge node")
}

func (e *Engine) registerKnowledgeExecutionGuard() {
	if e.Functions == nil {
		return
	}
	if e.Repo != nil {
		e.walkKnowledge("root", func(node *model.Node) {
			for _, tool := range node.Tools {
				e.knowledgeToolNames.Store(tool.Name, true)
			}
		})
	}
	e.Functions.SetExecutionGuard("agentize.knowledge", func(name string, args map[string]interface{}) error {
		return e.CheckKnowledgeToolAccess(stringArg(args, "__user_id__"), stringArg(args, "__session_id__"), name)
	})
}

func (e *Engine) openKnowledgePaths(session *model.Session) []string {
	paths := make([]string, 0, len(session.NodeDigests))
	for _, digest := range session.NodeDigests {
		if node, err := e.readableKnowledgeNode(session.UserID, digest.Path); err == nil {
			paths = append(paths, node.Path)
		}
	}
	return paths
}

func (e *Engine) walkVisibleKnowledge(userID, nodePath string, visit func(*model.Node)) {
	node, ok := e.visibleKnowledgeNode(userID, nodePath)
	if !ok {
		return
	}
	visit(node)
	children, err := e.Repo.GetChildren(node.Path)
	if err != nil {
		return
	}
	for _, child := range children {
		e.walkVisibleKnowledge(userID, child, visit)
	}
}
