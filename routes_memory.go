package agentize

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/gin-gonic/gin"
)

type memoryStore interface {
	GetUser(string) (*model.User, error)
	PutUser(*model.User) error
	GetUserSession(userID, sessionID string) (*model.Session, error)
	Put(*model.Session) error
}

func (ag *Agentize) memoryStore() (memoryStore, error) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		return nil, err
	}
	st, ok := handler.GetStore().(memoryStore)
	if !ok {
		return nil, errors.New("store does not support memory edits")
	}
	return st, nil
}

func (ag *Agentize) handleDebugUserSummaryDelete(c *gin.Context) {
	userID := c.Param("userID")
	index, _ := strconv.Atoi(c.Param("index"))
	if !confirmParam(c, userID) {
		rejectMemoryEdit(c, "delete_summary", userID, "confirmation mismatch")
		return
	}
	st, err := ag.memoryStore()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	user, err := st.GetUser(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	user.ContextSummary = model.RemoveSummaryEntry(user.ContextSummary, index)
	if err := st.PutUser(user); err != nil {
		failMemoryEdit(c, "delete_summary", userID, err)
		return
	}
	okMemoryEdit(c, "delete_summary", userID, "/agentize/debug/users/"+url.PathEscape(userID))
}

func (ag *Agentize) handleDebugUserTagDelete(c *gin.Context) {
	userID := c.Param("userID")
	if !confirmParam(c, userID) {
		rejectMemoryEdit(c, "delete_tag", userID, "confirmation mismatch")
		return
	}
	st, err := ag.memoryStore()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	user, err := st.GetUser(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	user.ContextTags = model.RemoveTag(user.ContextTags, strings.TrimSpace(c.PostForm("tag")))
	if err := st.PutUser(user); err != nil {
		failMemoryEdit(c, "delete_tag", userID, err)
		return
	}
	okMemoryEdit(c, "delete_tag", userID, "/agentize/debug/users/"+url.PathEscape(userID))
}

func (ag *Agentize) handleDebugUserTagEdit(c *gin.Context) {
	userID := c.Param("userID")
	if !confirmParam(c, userID) {
		rejectMemoryEdit(c, "edit_tag", userID, "confirmation mismatch")
		return
	}
	st, err := ag.memoryStore()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	user, err := st.GetUser(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	user.ContextTags = model.EditTag(user.ContextTags, c.PostForm("old_tag"), c.PostForm("new_tag"), model.MaxUserTags)
	if err := st.PutUser(user); err != nil {
		failMemoryEdit(c, "edit_tag", userID, err)
		return
	}
	okMemoryEdit(c, "edit_tag", userID, "/agentize/debug/users/"+url.PathEscape(userID))
}

func (ag *Agentize) handleDebugSessionSummaryDelete(c *gin.Context) {
	userID, sessionID := c.Param("userID"), c.Param("sessionID")
	index, _ := strconv.Atoi(c.Param("index"))
	if !confirmParam(c, sessionID) {
		rejectMemoryEdit(c, "delete_summary", userID, "confirmation mismatch")
		return
	}
	session, st, err := ag.loadDebugSession(userID, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	session.Summary = model.RemoveSummaryEntry(session.Summary, index)
	if err := st.Put(session); err != nil {
		failMemoryEdit(c, "delete_summary", userID, err)
		return
	}
	okMemoryEdit(c, "delete_summary", userID, "/agentize/debug/users/"+url.PathEscape(userID)+"/sessions/"+url.PathEscape(sessionID))
}

func (ag *Agentize) handleDebugSessionTagDelete(c *gin.Context) {
	userID, sessionID := c.Param("userID"), c.Param("sessionID")
	if !confirmParam(c, sessionID) {
		rejectMemoryEdit(c, "delete_tag", userID, "confirmation mismatch")
		return
	}
	session, st, err := ag.loadDebugSession(userID, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	session.Tags = model.RemoveTag(session.Tags, strings.TrimSpace(c.PostForm("tag")))
	if err := st.Put(session); err != nil {
		failMemoryEdit(c, "delete_tag", userID, err)
		return
	}
	okMemoryEdit(c, "delete_tag", userID, "/agentize/debug/users/"+url.PathEscape(userID)+"/sessions/"+url.PathEscape(sessionID))
}

func (ag *Agentize) handleDebugSessionTagEdit(c *gin.Context) {
	userID, sessionID := c.Param("userID"), c.Param("sessionID")
	if !confirmParam(c, sessionID) {
		rejectMemoryEdit(c, "edit_tag", userID, "confirmation mismatch")
		return
	}
	session, st, err := ag.loadDebugSession(userID, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	session.Tags = model.EditTag(session.Tags, c.PostForm("old_tag"), c.PostForm("new_tag"), model.MaxSessionTags)
	if err := st.Put(session); err != nil {
		failMemoryEdit(c, "edit_tag", userID, err)
		return
	}
	okMemoryEdit(c, "edit_tag", userID, "/agentize/debug/users/"+url.PathEscape(userID)+"/sessions/"+url.PathEscape(sessionID))
}

func (ag *Agentize) loadDebugSession(userID, sessionID string) (*model.Session, memoryStore, error) {
	st, err := ag.memoryStore()
	if err != nil {
		return nil, nil, err
	}
	session, err := st.GetUserSession(userID, sessionID)
	if err != nil {
		return nil, st, err
	}
	if session == nil || session.UserID != userID {
		return nil, st, nil
	}
	return session, st, nil
}

func confirmParam(c *gin.Context, want string) bool {
	return strings.TrimSpace(c.Query("confirm")) == want && want != ""
}

func rejectMemoryEdit(c *gin.Context, action, userID, reason string) {
	log.Log.Warnf("[Agentize] [AUDIT] %s REJECTED (%s) | user=%s ip=%s", action, reason, userID, c.ClientIP())
	metrics.AuditAction(action, "rejected")
	c.JSON(http.StatusBadRequest, gin.H{"error": "confirmation required"})
}

func failMemoryEdit(c *gin.Context, action, userID string, err error) {
	metrics.AuditAction(action, "error")
	log.Log.Errorf("[Agentize] [AUDIT] %s FAILED | user=%s ip=%s error=%v", action, userID, c.ClientIP(), err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func okMemoryEdit(c *gin.Context, action, userID, redirect string) {
	metrics.AuditAction(action, "ok")
	log.Log.Warnf("[Agentize] [AUDIT] %s OK | user=%s ip=%s", action, userID, c.ClientIP())
	c.Redirect(http.StatusFound, redirect)
}
