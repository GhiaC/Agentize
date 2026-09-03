package model

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"
)

const (
	// userIDMin and userIDMax are the inclusive bounds for Agentize-generated
	// user IDs: eight decimal digits, never zero-padded, never concatenated.
	userIDMin = 10_000_000
	userIDMax = 99_999_999
)

// FormatID returns seq as a decimal string with no prefix, padding, or parent
// fragment. Seq 1 is "1", not "0001" and not "{parent}-x0001".
func FormatID(seq int) string {
	if seq < 1 {
		return ""
	}
	return strconv.Itoa(seq)
}

// ParseID parses a numeric id produced by FormatID. Legacy concatenated ids
// return (0, false).
func ParseID(id string) (int, bool) {
	id = strings.TrimSpace(id)
	if id == "" || !IsNumericID(id) {
		return 0, false
	}
	n, err := strconv.Atoi(id)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// IsNumericID reports whether id is a pure decimal string (the current scheme).
func IsNumericID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// IsLegacyConcatID reports whether id uses the deprecated
// `{parent}-{kind}{seq}` form (hyphen plus a letter). Numeric ids and
// host-supplied user ids are not legacy.
func IsLegacyConcatID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || !strings.Contains(id, "-") {
		return false
	}
	for _, r := range id {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// SeqFromID returns the incremental sequence encoded in id.
// Numeric ids parse directly. Deprecated concatenated ids yield the trailing
// integer after the last "-s" / "-c" / "-m" / "-t" / "-f" / "-uf" / "-l" / "-rt"
// marker so empty seq columns can be filled without a data rewrite.
func SeqFromID(id string) int {
	id = strings.TrimSpace(id)
	if n, ok := ParseID(id); ok {
		return n
	}
	return parseLegacyConcatSeq(id)
}

func parseLegacyConcatSeq(id string) int {
	markers := []string{"-uf", "-rt", "-s", "-c", "-m", "-t", "-f", "-l"}
	best := -1
	markerLen := 0
	for _, m := range markers {
		if i := strings.LastIndex(id, m); i >= 0 && i >= best {
			best = i
			markerLen = len(m)
		}
	}
	if best < 0 || best+markerLen >= len(id) {
		return 0
	}
	n, err := strconv.Atoi(id[best+markerLen:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// GenerateUserID returns a random 8-digit decimal user id in [10000000, 99999999].
// User IDs are the exception to per-parent incrementing: they are random, not
// sequential, so users are not enumerable by counting.
func GenerateUserID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.Itoa(userIDMin)
	}
	n := binary.BigEndian.Uint64(buf[:]) % uint64(userIDMax-userIDMin+1)
	return strconv.FormatUint(uint64(userIDMin)+n, 10)
}

// IsGeneratedUserID reports whether id looks like an Agentize-assigned 8-digit user id.
func IsGeneratedUserID(id string) bool {
	n, ok := ParseID(id)
	return ok && n >= userIDMin && n <= userIDMax
}

// DisplayID is the operator-visible form of an Agentize entity id.
// Numeric ids are shown as-is (zero-padding stripped). Deprecated concatenated
// ids show only their sequence so the old `{parent}-{kind}{seq}` form is unused
// in dashboards. Host user ids, OpenAI tool_call ids, and other non-concat
// values are unchanged. Stored ids are not rewritten; lookups still use the
// raw value.
func DisplayID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if IsNumericID(id) {
		return FormatID(SeqFromID(id))
	}
	if IsLegacyConcatID(id) {
		if n := SeqFromID(id); n > 0 {
			return FormatID(n)
		}
	}
	return id
}

// ScopeKey is the in-process identity for a per-user incremental id (locks,
// queues, caches). It is not stored and is not a concatenated public ID.
func ScopeKey(userID, id string) string {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if userID == "" {
		return id
	}
	return userID + "/" + id
}

// EnsureID returns id when set, otherwise FormatID(seq). Used to fill empty
// identifiers on persist without rewriting legacy concatenated values.
func EnsureID(id string, seq int) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	return FormatID(seq)
}

// NextSeq increments *seq and returns the new value. *seq is treated as 0 when nil.
func NextSeq(seq *int) int {
	if seq == nil {
		return 1
	}
	*seq++
	if *seq < 1 {
		*seq = 1
	}
	return *seq
}
