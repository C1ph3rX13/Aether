package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	piCwdPreludeSchema    = "aether.pi-cwd/v1"
	maximumPiSessionBytes = 8 << 20
)

var canonicalPiEntryIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

func canonicalPiEntryID(value string) bool {
	return canonicalPiEntryIDPattern.MatchString(value)
}

type piCwdPrelude struct {
	Schema string `json:"schema"`
	CWD    string `json:"cwd"`
}

type piSessionHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type piSessionMessage struct {
	role       string
	stopReason string
	text       string
	pureText   bool
}

type piSessionEntry struct {
	typeName string
	id       string
	parentID *string
	message  *piSessionMessage
}

type piSessionDocument struct {
	header    piSessionHeader
	headerRaw []byte
	entries   []*piSessionEntry
	byID      map[string]*piSessionEntry
	leaf      *piSessionEntry
}

type piEvidenceState uint8

const (
	piEvidenceUnavailable piEvidenceState = iota
	piEvidenceComplete
	piEvidenceIncomplete
	piEvidenceBoundaryError
)

type piResultEvidence struct {
	state   piEvidenceState
	session *Session
}

type piStdoutEvidence struct {
	state     piEvidenceState
	prelude   piCwdPrelude
	header    piSessionHeader
	headerRaw []byte
	session   *Session
}

func validatePiStrictJSON(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("Pi JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumePiJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("Pi JSON has trailing data")
		}
		return err
	}
	return nil
}

func consumePiJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("Pi JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("Pi JSON object contains a duplicate field")
			}
			seen[key] = struct{}{}
			if err := consumePiJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("Pi JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err := consumePiJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("Pi JSON array is incomplete")
		}
	default:
		return fmt.Errorf("Pi JSON delimiter is invalid")
	}
	return nil
}

func decodePiRawObject(data []byte) (map[string]json.RawMessage, error) {
	if err := validatePiStrictJSON(data); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("Pi JSON record is not an object")
	}
	return object, nil
}

func decodePiCwdPrelude(data []byte) (piCwdPrelude, error) {
	object, err := decodePiRawObject(data)
	if err != nil {
		return piCwdPrelude{}, err
	}
	if len(object) != 2 {
		return piCwdPrelude{}, fmt.Errorf("Aether cwd prelude has unknown fields")
	}
	var prelude piCwdPrelude
	if err := json.Unmarshal(data, &prelude); err != nil || prelude.Schema != piCwdPreludeSchema ||
		!canonicalPiAbsolutePath(prelude.CWD) {
		return piCwdPrelude{}, fmt.Errorf("Aether cwd prelude is invalid")
	}
	return prelude, nil
}

func decodePiSessionHeader(data []byte) (piSessionHeader, error) {
	if _, err := decodePiRawObject(data); err != nil {
		return piSessionHeader{}, err
	}
	var header piSessionHeader
	if err := json.Unmarshal(data, &header); err != nil {
		return piSessionHeader{}, err
	}
	if err := validatePiSessionHeader(header); err != nil {
		return piSessionHeader{}, err
	}
	return header, nil
}

func validatePiSessionHeader(header piSessionHeader) error {
	parsedID, err := uuid.Parse(header.ID)
	if err != nil || parsedID.String() != header.ID || strings.ToLower(header.ID) != header.ID {
		return fmt.Errorf("Pi Session ID is not a canonical lowercase UUID")
	}
	if header.Type != "session" || header.Version != 3 || !canonicalPiTimestamp(header.Timestamp) ||
		!canonicalPiAbsolutePath(header.CWD) {
		return fmt.Errorf("Pi Session header is invalid")
	}
	return nil
}

func canonicalPiTimestamp(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z07:00", value)
	return err == nil && value == parsed.UTC().Format("2006-01-02T15:04:05.000Z")
}

func canonicalPiAbsolutePath(value string) bool {
	return utf8.ValidString(value) && value != "" && path.IsAbs(value) && path.Clean(value) == value &&
		strings.IndexByte(value, 0) < 0
}

func parsePiSessionDocument(data []byte) (*piSessionDocument, error) {
	if len(data) > maximumPiSessionBytes {
		return nil, fmt.Errorf("Pi Session exceeds its limit")
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("Pi Session has an incomplete final line")
	}
	physicalLines := bytes.Split(data, []byte{'\n'})
	records := make([][]byte, 0, len(physicalLines))
	for _, line := range physicalLines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		records = append(records, append([]byte(nil), line...))
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("Pi Session is empty")
	}
	header, err := decodePiSessionHeader(records[0])
	if err != nil {
		return nil, err
	}
	document := &piSessionDocument{
		header: header, headerRaw: records[0], byID: make(map[string]*piSessionEntry),
	}
	for _, record := range records[1:] {
		entry, err := decodePiSessionEntry(record)
		if err != nil {
			return nil, err
		}
		if _, duplicate := document.byID[entry.id]; duplicate {
			return nil, fmt.Errorf("Pi Session contains duplicate entry IDs")
		}
		document.byID[entry.id] = entry
		document.entries = append(document.entries, entry)
		document.leaf = entry
	}
	if err := validatePiSessionTree(document); err != nil {
		return nil, err
	}
	return document, nil
}

func decodePiSessionEntry(data []byte) (*piSessionEntry, error) {
	object, err := decodePiRawObject(data)
	if err != nil {
		return nil, err
	}
	var typeName, id, timestamp string
	if err := decodePiRequiredString(object, "type", &typeName); err != nil || typeName == "session" {
		return nil, fmt.Errorf("Pi Session entry type is invalid")
	}
	if err := decodePiRequiredString(object, "id", &id); err != nil || !canonicalPiEntryID(id) {
		return nil, fmt.Errorf("Pi Session entry ID is invalid")
	}
	if err := decodePiRequiredString(object, "timestamp", &timestamp); err != nil || !canonicalPiTimestamp(timestamp) {
		return nil, fmt.Errorf("Pi Session entry timestamp is invalid")
	}
	parentRaw, exists := object["parentId"]
	if !exists {
		return nil, fmt.Errorf("Pi Session entry parent is missing")
	}
	var parentID *string
	if !bytes.Equal(bytes.TrimSpace(parentRaw), []byte("null")) {
		var value string
		if err := json.Unmarshal(parentRaw, &value); err != nil || !canonicalPiEntryID(value) {
			return nil, fmt.Errorf("Pi Session entry parent is invalid")
		}
		parentID = &value
	}
	entry := &piSessionEntry{typeName: typeName, id: id, parentID: parentID}
	if typeName == "message" {
		messageRaw, exists := object["message"]
		if !exists {
			return nil, fmt.Errorf("Pi Session message is missing")
		}
		message, err := decodePiSessionMessage(messageRaw)
		if err != nil {
			return nil, err
		}
		entry.message = &message
	}
	return entry, nil
}

func decodePiRequiredString(object map[string]json.RawMessage, key string, target *string) error {
	raw, exists := object[key]
	if !exists || json.Unmarshal(raw, target) != nil || *target == "" || !utf8.ValidString(*target) {
		return fmt.Errorf("Pi JSON field %s is invalid", key)
	}
	return nil
}

func decodePiSessionMessage(data []byte) (piSessionMessage, error) {
	object, err := decodePiRawObject(data)
	if err != nil {
		return piSessionMessage{}, err
	}
	var message piSessionMessage
	if err := decodePiRequiredString(object, "role", &message.role); err != nil {
		return piSessionMessage{}, err
	}
	if message.role != "user" && message.role != "assistant" && message.role != "toolResult" && message.role != "custom" {
		return piSessionMessage{}, fmt.Errorf("Pi Session message role is invalid")
	}
	if _, exists := object["stopReason"]; exists {
		if err := decodePiRequiredStringAllowEmpty(object, "stopReason", &message.stopReason); err != nil {
			return piSessionMessage{}, fmt.Errorf("Pi Session stop reason is invalid")
		}
	}
	content, exists := object["content"]
	if !exists {
		return piSessionMessage{}, fmt.Errorf("Pi Session message content is missing")
	}
	text, pureText, err := decodePiMessageContent(content)
	if err != nil {
		return piSessionMessage{}, err
	}
	message.text = text
	message.pureText = pureText
	return message, nil
}

func decodePiMessageContent(data []byte) (string, bool, error) {
	data = bytes.TrimSpace(data)
	var plain string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &plain); err != nil || !utf8.ValidString(plain) {
			return "", false, fmt.Errorf("Pi Session message text is invalid")
		}
		return plain, true, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil || blocks == nil {
		return "", false, fmt.Errorf("Pi Session message content is invalid")
	}
	var text strings.Builder
	pureText := len(blocks) > 0
	for _, block := range blocks {
		object, err := decodePiRawObject(block)
		if err != nil {
			return "", false, err
		}
		var typeName string
		if err := decodePiRequiredString(object, "type", &typeName); err != nil {
			return "", false, err
		}
		if typeName != "text" {
			pureText = false
			continue
		}
		var value string
		if err := decodePiRequiredStringAllowEmpty(object, "text", &value); err != nil {
			return "", false, err
		}
		if text.Len()+len(value) > maximumPiResponseBytes {
			return "", false, fmt.Errorf("Pi Session assistant text exceeds its limit")
		}
		text.WriteString(value)
	}
	return text.String(), pureText, nil
}

func decodePiRequiredStringAllowEmpty(object map[string]json.RawMessage, key string, target *string) error {
	raw, exists := object[key]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil || !utf8.ValidString(*target) {
		return fmt.Errorf("Pi JSON field %s is invalid", key)
	}
	return nil
}

func validatePiSessionTree(document *piSessionDocument) error {
	roots := 0
	for _, entry := range document.entries {
		if entry.parentID == nil {
			roots++
			continue
		}
		if _, exists := document.byID[*entry.parentID]; !exists {
			return fmt.Errorf("Pi Session entry parent is missing")
		}
	}
	if len(document.entries) > 0 && roots != 1 {
		return fmt.Errorf("Pi Session tree root is ambiguous")
	}
	states := make(map[string]uint8, len(document.entries))
	var visit func(*piSessionEntry) error
	visit = func(entry *piSessionEntry) error {
		switch states[entry.id] {
		case 1:
			return fmt.Errorf("Pi Session tree contains a cycle")
		case 2:
			return nil
		}
		states[entry.id] = 1
		if entry.parentID != nil {
			if err := visit(document.byID[*entry.parentID]); err != nil {
				return err
			}
		}
		states[entry.id] = 2
		return nil
	}
	for _, entry := range document.entries {
		if err := visit(entry); err != nil {
			return err
		}
	}
	return nil
}

func verifyPiInvocationSuffix(
	invocation Invocation,
	document *piSessionDocument,
	observed *Session,
) piResultEvidence {
	session := *observed
	if invocation.session != nil {
		if invocation.session.value != session.value {
			return piResultEvidence{state: piEvidenceBoundaryError, session: &session}
		}
		session.cursor = invocation.session.cursor
		session.hasCursor = invocation.session.hasCursor
	}
	branch := make([]*piSessionEntry, 0, len(document.entries))
	for current := document.leaf; current != nil; {
		branch = append(branch, current)
		if current.parentID == nil {
			break
		}
		current = document.byID[*current.parentID]
	}
	for left, right := 0, len(branch)-1; left < right; left, right = left+1, right-1 {
		branch[left], branch[right] = branch[right], branch[left]
	}
	start := 0
	if session.hasCursor && session.cursor != "" {
		start = -1
		for index, entry := range branch {
			if entry.id == session.cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return piResultEvidence{state: piEvidenceBoundaryError, session: &session}
		}
	}
	suffix := branch[start:]
	userMessages := 0
	var lastMessage *piSessionMessage
	for _, entry := range suffix {
		if entry.message == nil {
			continue
		}
		lastMessage = entry.message
		if entry.message.role != "user" {
			continue
		}
		userMessages++
		if !entry.message.pureText || sha256.Sum256([]byte(entry.message.text)) != sha256.Sum256([]byte(invocation.prompt)) {
			return piResultEvidence{state: piEvidenceBoundaryError, session: &session}
		}
	}
	if userMessages != 1 || document.leaf == nil {
		return piResultEvidence{state: piEvidenceBoundaryError, session: &session}
	}
	session.cursor = document.leaf.id
	session.hasCursor = true
	if lastMessage == nil || lastMessage.role != "assistant" || lastMessage.stopReason != "stop" ||
		strings.TrimSpace(lastMessage.text) == "" {
		return piResultEvidence{state: piEvidenceIncomplete, session: &session}
	}
	return piResultEvidence{state: piEvidenceComplete, session: &session}
}
