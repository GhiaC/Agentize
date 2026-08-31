package engine

// QueueClass selects which session queue an inbound message joins while a turn
// is already running.
type QueueClass string

const (
	// QueueUser is a live user follow-up. It is injected into the in-flight
	// session between tool rounds and processed on the next LLM iteration.
	QueueUser QueueClass = "user"
	// QueueDeferred is an alert or schedule. It waits until the current turn
	// and every tool call have finished, then starts as its own turn.
	QueueDeferred QueueClass = "deferred"
)

const queuedAckMessage = "⏳ Processing previous request... Please wait. 📋 Your message was queued and will be answered in order."

// IncomingMessage is one chat turn input, including optional durable metadata
// for alert/schedule origin (later used by host widgets).
type IncomingMessage struct {
	Content  string
	Metadata map[string]any
	Queue    QueueClass
}

func (m IncomingMessage) queueClass() QueueClass {
	if m.Queue == QueueDeferred {
		return QueueDeferred
	}
	return QueueUser
}

func cloneIncomingMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		out[key] = value
	}
	return out
}
