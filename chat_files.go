package agentize

import "github.com/ghiac/agentize/filemanager"

// ChatFiles composes conversation ingestion and delivery over the same metadata
// and byte stores used by every worker and the user-facing file manager.
func (ag *Agentize) ChatFiles() *filemanager.ChatService {
	if ag == nil || ag.engine == nil {
		return nil
	}
	return &filemanager.ChatService{Store: ag.engine.Sessions, Files: ag.engine.Files}
}
