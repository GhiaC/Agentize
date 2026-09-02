package core

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// TestSystemPromptSectionsFor_KeysAndClassification checks that the structured
// view exposes every section with correct identity/classification and that the
// required prefix is byte-exact and included.
func TestSystemPromptSectionsFor_KeysAndClassification(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	ch.coreSessions["user1"] = &model.Session{
		SessionID: "s1",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   model.SummaryEntries{"the user likes go"},
		Tags:      []string{"golang"},
	}

	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}

	byKey := map[string]model.PromptSection{}
	for _, s := range sections {
		byKey[s.Key] = s
	}

	for _, k := range []string{SectionCoreController} {
		s, ok := byKey[k]
		if !ok {
			t.Fatalf("missing required section %q", k)
		}
		if !s.Required || s.Dynamic {
			t.Errorf("section %q should be Required+Static", k)
		}
		if !s.Included {
			t.Errorf("required section %q should be Included", k)
		}
	}

	if got := byKey[SectionCoreController]; got.Content != coreControllerPrompt {
		t.Error("core_controller content must equal the embedded controller prompt")
	} else if got.Bytes != len(coreControllerPrompt) {
		t.Errorf("Bytes (%d) must equal len(Content) (%d)", got.Bytes, len(coreControllerPrompt))
	}

	cs, ok := byKey[SectionCoreSessionContext]
	if !ok {
		t.Fatal("missing core_session_context section")
	}
	if cs.Required || !cs.Dynamic {
		t.Error("core_session_context should be Optional+Dynamic")
	}
	if !cs.Included || !strings.Contains(cs.Content, "the user likes go") {
		t.Error("core_session_context should be included and carry the summary")
	}

	if _, ok := byKey["agents"]; ok {
		t.Error("agent catalog must be a tool concern, not prompt content")
	}
}

func TestRemovedCorePromptCollectionsStayOutOfPrompt(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	ch.config.AppPolicy = "must not be injected"
	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}
	removed := map[string]bool{"app_policy": true, "agents": true, "agent_tools": true, "agent_session_contexts": true, "user_files": true, "active_sessions": true, "sessions_list": true, "conversations": true}
	for _, section := range sections {
		if removed[section.Key] {
			t.Errorf("removed section %q was assembled", section.Key)
		}
	}
}

// TestSystemPromptSectionsFor_DropsOverBudget mirrors the live-array budget test:
// an over-budget optional section is marked not-included but still exposes its
// content so the UI can show what was cut.
func TestSystemPromptSectionsFor_DropsOverBudget(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	huge := strings.Repeat("x", 5000)
	ch.coreSessions["user1"] = &model.Session{
		SessionID: "s1",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   model.SummaryEntries{huge},
	}
	required := len(coreControllerPrompt)
	ch.config.MaxSystemPromptSize = required + 1000 // room for small sections, not the 5000-char summary

	sections, err := ch.SystemPromptSectionsFor("user1")
	if err != nil {
		t.Fatalf("SystemPromptSectionsFor: %v", err)
	}

	var ctx *model.PromptSection
	for i := range sections {
		if sections[i].Key == SectionCoreSessionContext {
			ctx = &sections[i]
		}
	}
	if ctx == nil {
		t.Fatal("core_session_context section missing")
	}
	if ctx.Included {
		t.Error("over-budget core_session_context must be marked not included")
	}
	if ctx.Content == "" {
		t.Error("a dropped section should still expose its content for display")
	}

	// And the live array (which projects the same assembly) must not contain it.
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	if strings.Contains(strings.Join(prompts, " "), huge[:100]) {
		t.Error("the live array must drop the over-budget section too")
	}
}

// TestPreviewSystemPromptSections_FromStore checks the store-only preview: the
// real controller and the user's stored Core session surface, while
// agent-dependent sections are empty and annotated (no registered agents).
func TestPreviewSystemPromptSections_FromStore(t *testing.T) {
	_, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	if err := sqliteStore.Put(&model.Session{
		SessionID: "core-s",
		UserID:    "user1",
		AgentType: model.AgentTypeCore,
		Summary:   model.SummaryEntries{"stored summary"},
		Tags:      []string{"topic"},
	}); err != nil {
		t.Fatalf("seed core session: %v", err)
	}

	sections, err := PreviewSystemPromptSections(sqliteStore, "user1", CoreHandlerConfig{})
	if err != nil {
		t.Fatalf("PreviewSystemPromptSections: %v", err)
	}

	byKey := map[string]model.PromptSection{}
	for _, s := range sections {
		byKey[s.Key] = s
	}

	if byKey[SectionCoreController].Content != coreControllerPrompt {
		t.Error("preview must include the real embedded controller prompt")
	}
	if !strings.Contains(byKey[SectionCoreSessionContext].Content, "stored summary") {
		t.Error("preview should surface the stored Core session summary")
	}

}
