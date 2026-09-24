package engine

import "sync"

// ProgressGuard holds per-key in-progress flag and two message queues.
// User follow-ups are injected between tool rounds. Alert/schedule messages
// wait on the deferred queue until the current turn (including tools) is idle.
// Safe for use by CoreHandler (key=userID) and Engine (key=sessionID).
type ProgressGuard struct {
	mu    sync.RWMutex
	state map[string]*progressState
}

// QueuedMessage is one deferred or in-turn follow-up waiting on a busy key.
type QueuedMessage struct {
	Content  string
	Metadata map[string]any
}

type progressState struct {
	InProgress bool
	Queue      []QueuedMessage
	Deferred   []QueuedMessage
}

// maxQueuedPerKey bounds each per-key queue so a client hammering the
// endpoint while a request is in flight cannot grow memory without limit.
// Messages beyond the cap are dropped (the caller still gets the "in progress"
// response — the oldest queued messages are kept).
const maxQueuedPerKey = 20

// NewProgressGuard returns a new ProgressGuard.
func NewProgressGuard() *ProgressGuard {
	return &ProgressGuard{state: make(map[string]*progressState)}
}

// TryQueue queues a user follow-up and returns true if the key is already in
// progress. Returns false if the key is idle (caller should process now).
func (p *ProgressGuard) TryQueue(key, message string) (queued bool) {
	queued, _ = p.TryQueueMessage(key, QueuedMessage{Content: message}, QueueUser)
	return queued
}

// TryQueueDeferred queues an alert/schedule message while the key is busy.
func (p *ProgressGuard) TryQueueDeferred(key, message string) (queued bool) {
	queued, _ = p.TryQueueMessage(key, QueuedMessage{Content: message}, QueueDeferred)
	return queued
}

// TryQueueMessage queues IncomingMessage-shaped content onto the user or
// deferred queue. queued is true when the message was stored. rejected is true
// when the key is busy and the queue is already at capacity; nothing is stored.
func (p *ProgressGuard) TryQueueMessage(key string, message QueuedMessage, class QueueClass) (queued, rejected bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state[key]
	if s == nil || !s.InProgress {
		return false, false
	}
	message.Metadata = cloneIncomingMeta(message.Metadata)
	if class == QueueDeferred {
		if len(s.Deferred) >= maxQueuedPerKey {
			return false, true
		}
		s.Deferred = append(s.Deferred, message)
		return true, false
	}
	if len(s.Queue) >= maxQueuedPerKey {
		return false, true
	}
	s.Queue = append(s.Queue, message)
	return true, false
}

// SetInProgress sets the in-progress flag for the key. Call when starting/ending
// processing while holding the process mutex.
func (p *ProgressGuard) SetInProgress(key string, inProgress bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state[key] == nil {
		p.state[key] = &progressState{}
	}
	p.state[key].InProgress = inProgress
}

// DrainQueue returns and clears the user follow-up queue for the key.
func (p *ProgressGuard) DrainQueue(key string) []QueuedMessage {
	return p.drain(key, false)
}

// DrainDeferred returns and clears the alert/schedule queue for the key.
func (p *ProgressGuard) DrainDeferred(key string) []QueuedMessage {
	return p.drain(key, true)
}

// TakeUser pops the next user follow-up, if any.
func (p *ProgressGuard) TakeUser(key string) (QueuedMessage, bool) {
	return p.take(key, false)
}

// TakeDeferred pops the next alert/schedule message, if any.
func (p *ProgressGuard) TakeDeferred(key string) (QueuedMessage, bool) {
	return p.take(key, true)
}

func (p *ProgressGuard) drain(key string, deferred bool) []QueuedMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state[key]
	if s == nil {
		return nil
	}
	if deferred {
		if len(s.Deferred) == 0 {
			return nil
		}
		out := s.Deferred
		s.Deferred = nil
		return out
	}
	if len(s.Queue) == 0 {
		return nil
	}
	out := s.Queue
	s.Queue = nil
	return out
}

func (p *ProgressGuard) take(key string, deferred bool) (QueuedMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state[key]
	if s == nil {
		return QueuedMessage{}, false
	}
	src := &s.Queue
	if deferred {
		src = &s.Deferred
	}
	if len(*src) == 0 {
		return QueuedMessage{}, false
	}
	out := (*src)[0]
	*src = (*src)[1:]
	return out, true
}

func queuedContents(items []QueuedMessage) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Content
	}
	return out
}

func incomingFromQueued(item QueuedMessage, class QueueClass) IncomingMessage {
	return IncomingMessage{Content: item.Content, Metadata: item.Metadata, Queue: class}
}
