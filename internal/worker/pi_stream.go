package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

const (
	maximumPiEventLineBytes = 512 * 1024
	maximumPiResponseBytes  = 256 * 1024
	maximumPiMetadataBytes  = 4 * 1024
	maximumPiNumberBytes    = 128
)

// piOutputCapture consumes Pi's JSONL transport without retaining the
// transcript. It keeps the schema-bound launch records and bounded terminal
// lifecycle evidence needed for Session boundary verification.
type piOutputCapture struct {
	mu sync.Mutex

	line                []byte
	longLine            *piEventStream
	droppingLine        bool
	transportOverflowed bool

	recordCount        int
	prelude            piCwdPrelude
	header             piSessionHeader
	headerRaw          []byte
	boundaryError      bool
	lifecycleMalformed bool
	eventSequence      int
	agentStarted       bool
	agentEnded         bool
	messageEnd         *piLifecycleMessage
	messageEndSequence int
	turnEnd            *piLifecycleMessage
	turnEndSequence    int
	agentEnd           *piLifecycleMessage
	agentEndSequence   int

	session          *Session
	responseSeen     bool
	response         []byte
	responseOverflow bool
	fileEvidenceRead bool
	fileSession      piResultEvidence
	fileResult       rundomain.StoppedProcessFile
}

type piLifecycleMessage struct {
	text       string
	stopReason string
}

func newPiOutputCapture(session *Session) *piOutputCapture {
	capture := &piOutputCapture{}
	if session != nil && strings.TrimSpace(session.value) != "" {
		copy := *session
		capture.session = &copy
	}
	return capture
}

func (capture *piOutputCapture) WriteStdout(data []byte) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			capture.appendLineFragment(data)
			return
		}
		capture.appendLineFragment(data[:newline])
		capture.finishLine()
		data = data[newline+1:]
	}
}

func (capture *piOutputCapture) WriteStderr(_ []byte) {
	// Pi stderr remains in Runtime's bounded diagnostic collector. It is not a
	// source of business results or sessions.
}

func (capture *piOutputCapture) appendLineFragment(fragment []byte) {
	if capture.longLine != nil {
		if capture.longLine.failed {
			return
		}
		if err := capture.longLine.write(fragment); err != nil {
			capture.longLine.failed = true
		}
		return
	}
	if capture.droppingLine {
		return
	}
	remaining := maximumPiEventLineBytes - len(capture.line)
	if len(fragment) > remaining {
		capture.transportOverflowed = true
		if capture.recordCount < 2 {
			capture.boundaryError = true
		}
		if capture.startLargeAgentEnd(fragment) {
			return
		}
		capture.droppingLine = true
		return
	}
	capture.line = append(capture.line, fragment...)
}

func (capture *piOutputCapture) finishLine() {
	if capture.longLine != nil {
		stream := capture.longLine
		capture.longLine = nil
		_ = stream.writer.Close()
		result := <-stream.result
		if result.err == nil {
			if capture.recordCount < 2 {
				capture.boundaryError = true
			} else {
				capture.observeEvent(result.event)
			}
		} else {
			capture.lifecycleMalformed = true
		}
		capture.line = nil
		capture.droppingLine = false
		return
	}
	if capture.droppingLine {
		capture.line = nil
		capture.droppingLine = false
		return
	}
	capture.consumeLine(capture.line)
	capture.line = nil
}

// startLargeAgentEnd streams any known final-message event that exceeds the
// ordinary transport line limit. The name is retained because agent_end is
// the usual large event; message_end and turn_end need the same candidate
// size semantics.
func (capture *piOutputCapture) startLargeAgentEnd(fragment []byte) bool {
	prefixLength := maximumPiEventLineBytes - len(capture.line)
	if prefixLength > len(fragment) {
		prefixLength = len(fragment)
	}
	prefixCopy := append([]byte(nil), capture.line...)
	prefixCopy = append(prefixCopy, fragment[:prefixLength]...)
	prefix := bytes.TrimSpace(prefixCopy)
	if !bytes.HasPrefix(prefix, []byte(`{"type":"agent_end"`)) &&
		!bytes.HasPrefix(prefix, []byte(`{"type":"message_end"`)) &&
		!bytes.HasPrefix(prefix, []byte(`{"type":"turn_end"`)) &&
		!bytes.HasPrefix(prefix, []byte(`{"message":`)) &&
		!bytes.HasPrefix(prefix, []byte(`{"messages":[`)) {
		return false
	}
	capture.longLine = newPiEventStream()
	capture.line = nil
	if err := capture.longLine.write(prefixCopy); err != nil {
		capture.longLine.failed = true
		return true
	}
	if prefixLength < len(fragment) {
		if err := capture.longLine.write(fragment[prefixLength:]); err != nil {
			capture.longLine.failed = true
		}
	}
	return true
}

type piEventStream struct {
	writer *io.PipeWriter
	result chan piEventStreamResult
	failed bool
}

type piEventStreamResult struct {
	event piEvent
	err   error
}

func newPiEventStream() *piEventStream {
	reader, writer := io.Pipe()
	stream := &piEventStream{writer: writer, result: make(chan piEventStreamResult, 1)}
	go func() {
		event, err := decodePiEvent(reader)
		if err != nil {
			// A malformed large line must not stop the producer from draining
			// the Docker frame. Continue consuming until the writer closes.
			_, _ = io.Copy(io.Discard, reader)
		}
		_ = reader.Close()
		stream.result <- piEventStreamResult{event: event, err: err}
	}()
	return stream
}

func (stream *piEventStream) write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := stream.writer.Write(data)
	return err
}

// piJSONDecoder parses only the event fields needed for session and final
// assistant capture. Unknown JSON values are consumed structurally, so a
// large transcript or tool payload is never materialized as one object.
type piJSONDecoder struct {
	reader *bufio.Reader
}

func decodePiEvent(reader io.Reader) (piEvent, error) {
	decoder := &piJSONDecoder{reader: bufio.NewReader(reader)}
	var event piEvent
	if err := decoder.readObject(func(key string) error {
		switch key {
		case "type":
			value, overflow, err := decoder.readString(maximumPiMetadataBytes)
			if err != nil {
				return err
			}
			if !overflow {
				event.Type = value
			}
		case "id":
			value, overflow, err := decoder.readString(maximumPiMetadataBytes)
			if err != nil {
				return err
			}
			if !overflow {
				event.ID = value
			}
		case "message":
			message, err := decoder.readMessage()
			if err != nil {
				return err
			}
			event.Message = &message
		case "messages":
			message, err := decoder.readLatestAssistant()
			if err != nil {
				return err
			}
			if message != nil {
				event.Messages = []piMessage{*message}
			}
		default:
			if err := decoder.skipValue(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return piEvent{}, err
	}
	if err := decoder.requireEOF(); err != nil {
		return piEvent{}, err
	}
	return event, nil
}

func (decoder *piJSONDecoder) readLatestAssistant() (*piMessage, error) {
	var latest *piMessage
	if err := decoder.readArray(func() error {
		message, err := decoder.readMessage()
		if err != nil {
			return err
		}
		if message.Role == "assistant" {
			copy := message
			latest = &copy
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return latest, nil
}

func (decoder *piJSONDecoder) readMessage() (piMessage, error) {
	var message piMessage
	if err := decoder.readObject(func(key string) error {
		switch key {
		case "role":
			value, overflow, err := decoder.readString(maximumPiMetadataBytes)
			if err != nil {
				return err
			}
			if !overflow {
				message.Role = value
			}
		case "stopReason":
			value, overflow, err := decoder.readString(maximumPiMetadataBytes)
			if err != nil {
				return err
			}
			if !overflow {
				message.StopReason = value
			}
		case "content":
			content, overflow, err := decoder.readContent()
			if err != nil {
				return err
			}
			message.Content = content
			message.overflow = overflow
		default:
			if err := decoder.skipValue(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return piMessage{}, err
	}
	return message, nil
}

func (decoder *piJSONDecoder) readContent() ([]piContent, bool, error) {
	content := make([]piContent, 0)
	overflowed := false
	textBytes := 0
	if err := decoder.readArray(func() error {
		item, err := decoder.readContentItem()
		if err != nil {
			return err
		}
		if item.Type != "text" || item.Text == "" {
			return nil
		}
		if item.overflow {
			overflowed = true
			return nil
		}
		additional := len(item.Text)
		if textBytes+additional > maximumPiResponseBytes {
			overflowed = true
			return nil
		}
		textBytes += additional
		content = append(content, item)
		return nil
	}); err != nil {
		return nil, false, err
	}
	return content, overflowed, nil
}

func (decoder *piJSONDecoder) readContentItem() (piContent, error) {
	var item piContent
	if err := decoder.readObject(func(key string) error {
		switch key {
		case "type":
			value, overflow, err := decoder.readString(maximumPiMetadataBytes)
			if err != nil {
				return err
			}
			if !overflow {
				item.Type = value
			}
		case "text":
			value, overflow, err := decoder.readString(maximumPiResponseBytes + 1)
			if err != nil {
				return err
			}
			item.Text = value
			item.overflow = overflow
		default:
			if err := decoder.skipValue(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return piContent{}, err
	}
	return item, nil
}

func (decoder *piJSONDecoder) readObject(handle func(string) error) error {
	opening, err := decoder.readNonSpace()
	if err != nil {
		return err
	}
	if opening != '{' {
		return fmt.Errorf("Pi JSON value is not an object")
	}
	separator, err := decoder.readNonSpace()
	if err != nil {
		return err
	}
	if separator == '}' {
		return nil
	}
	if err := decoder.reader.UnreadByte(); err != nil {
		return err
	}
	seen := make(map[string]struct{})
	for {
		key, overflow, err := decoder.readString(maximumPiMetadataBytes)
		if err != nil {
			return err
		}
		if overflow {
			return fmt.Errorf("Pi JSON object key exceeds its limit")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("Pi JSON object contains a duplicate field")
		}
		seen[key] = struct{}{}
		colon, err := decoder.readNonSpace()
		if err != nil {
			return err
		}
		if colon != ':' {
			return fmt.Errorf("Pi JSON object key is not followed by a colon")
		}
		if err := handle(key); err != nil {
			return err
		}
		separator, err := decoder.readNonSpace()
		if err != nil {
			return err
		}
		if separator == '}' {
			return nil
		}
		if separator != ',' {
			return fmt.Errorf("Pi JSON object is not comma separated")
		}
		separator, err = decoder.readNonSpace()
		if err != nil {
			return err
		}
		if separator == '}' {
			return fmt.Errorf("Pi JSON object has a trailing comma")
		}
		if err := decoder.reader.UnreadByte(); err != nil {
			return err
		}
	}
}

func (decoder *piJSONDecoder) readArray(handle func() error) error {
	opening, err := decoder.readNonSpace()
	if err != nil {
		return err
	}
	if opening != '[' {
		return fmt.Errorf("Pi JSON value is not an array")
	}
	separator, err := decoder.readNonSpace()
	if err != nil {
		return err
	}
	if separator == ']' {
		return nil
	}
	if err := decoder.reader.UnreadByte(); err != nil {
		return err
	}
	for {
		if err := handle(); err != nil {
			return err
		}
		separator, err := decoder.readNonSpace()
		if err != nil {
			return err
		}
		if separator == ']' {
			return nil
		}
		if separator != ',' {
			return fmt.Errorf("Pi JSON array is not comma separated")
		}
		separator, err = decoder.readNonSpace()
		if err != nil {
			return err
		}
		if separator == ']' {
			return fmt.Errorf("Pi JSON array has a trailing comma")
		}
		if err := decoder.reader.UnreadByte(); err != nil {
			return err
		}
	}
}

func (decoder *piJSONDecoder) skipValue() error {
	first, err := decoder.readNonSpace()
	if err != nil {
		return err
	}
	switch first {
	case '{':
		if err := decoder.reader.UnreadByte(); err != nil {
			return err
		}
		return decoder.readObject(func(string) error { return decoder.skipValue() })
	case '[':
		if err := decoder.reader.UnreadByte(); err != nil {
			return err
		}
		return decoder.readArray(decoder.skipValue)
	case '"':
		if err := decoder.reader.UnreadByte(); err != nil {
			return err
		}
		_, _, err := decoder.readString(0)
		return err
	case 't':
		return decoder.readLiteral("rue")
	case 'f':
		return decoder.readLiteral("alse")
	case 'n':
		return decoder.readLiteral("ull")
	default:
		if first == '-' || first >= '0' && first <= '9' {
			return decoder.readNumber(first)
		}
		return fmt.Errorf("Pi JSON value is invalid")
	}
}

func (decoder *piJSONDecoder) readString(limit int) (string, bool, error) {
	opening, err := decoder.readNonSpace()
	if err != nil {
		return "", false, err
	}
	if opening != '"' {
		return "", false, fmt.Errorf("Pi JSON value is not a string")
	}
	var value strings.Builder
	overflowed := false
	appendValue := func(text string) {
		if overflowed || limit == 0 {
			if len(text) > 0 {
				overflowed = true
			}
			return
		}
		if value.Len()+len(text) > limit {
			overflowed = true
			return
		}
		value.WriteString(text)
	}
	for {
		character, err := decoder.reader.ReadByte()
		if err != nil {
			return "", false, err
		}
		switch character {
		case '"':
			if !overflowed && !utf8.ValidString(value.String()) {
				return "", false, fmt.Errorf("Pi JSON string is not valid UTF-8")
			}
			return value.String(), overflowed, nil
		case '\\':
			escaped, err := decoder.readEscape()
			if err != nil {
				return "", false, err
			}
			appendValue(escaped)
		default:
			if character < 0x20 {
				return "", false, fmt.Errorf("Pi JSON string contains a control character")
			}
			appendValue(string(character))
		}
	}
}

func (decoder *piJSONDecoder) readEscape() (string, error) {
	character, err := decoder.reader.ReadByte()
	if err != nil {
		return "", err
	}
	switch character {
	case '"', '\\', '/':
		return string(character), nil
	case 'b':
		return "\b", nil
	case 'f':
		return "\f", nil
	case 'n':
		return "\n", nil
	case 'r':
		return "\r", nil
	case 't':
		return "\t", nil
	case 'u':
		value, err := decoder.readHexRune()
		if err != nil {
			return "", err
		}
		if value >= 0xd800 && value <= 0xdbff {
			first, err := decoder.reader.ReadByte()
			if err == nil && first == '\\' {
				second, secondErr := decoder.reader.ReadByte()
				if secondErr == nil && second == 'u' {
					low, lowErr := decoder.readHexRune()
					if lowErr == nil && low >= 0xdc00 && low <= 0xdfff {
						return string(utf16.DecodeRune(rune(value), rune(low))), nil
					}
					return "", fmt.Errorf("Pi JSON Unicode surrogate pair is invalid")
				}
				return "", fmt.Errorf("Pi JSON Unicode escape is invalid")
			}
			if err == nil {
				if err := decoder.reader.UnreadByte(); err != nil {
					return "", err
				}
			}
			return "", fmt.Errorf("Pi JSON Unicode surrogate pair is invalid")
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return "", fmt.Errorf("Pi JSON Unicode surrogate pair is invalid")
		}
		return string(rune(value)), nil
	default:
		return "", fmt.Errorf("Pi JSON escape is invalid")
	}
}

func (decoder *piJSONDecoder) readHexRune() (uint16, error) {
	var value uint16
	for range 4 {
		character, err := decoder.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value += uint16(character-'A') + 10
		default:
			return 0, fmt.Errorf("Pi JSON Unicode escape is invalid")
		}
	}
	return value, nil
}

func (decoder *piJSONDecoder) readLiteral(suffix string) error {
	for index := range suffix {
		character, err := decoder.reader.ReadByte()
		if err != nil {
			return err
		}
		if character != suffix[index] {
			return fmt.Errorf("Pi JSON literal is invalid")
		}
	}
	return nil
}

func (decoder *piJSONDecoder) readNumber(first byte) error {
	number := make([]byte, 0, maximumPiNumberBytes)
	number = append(number, first)
	overflowed := false
	for {
		peeked, err := decoder.reader.Peek(1)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		character := peeked[0]
		if character == ',' || character == ']' || character == '}' || isPiJSONSpace(character) {
			break
		}
		if len(number) < maximumPiNumberBytes {
			number = append(number, character)
		} else {
			overflowed = true
		}
		if _, err := decoder.reader.ReadByte(); err != nil {
			return err
		}
	}
	if overflowed || !validPiJSONNumber(number) {
		return fmt.Errorf("Pi JSON number is invalid")
	}
	return nil
}

func validPiJSONNumber(number []byte) bool {
	index := 0
	if index < len(number) && number[index] == '-' {
		index++
	}
	if index >= len(number) {
		return false
	}
	if number[index] == '0' {
		index++
	} else if number[index] >= '1' && number[index] <= '9' {
		for index < len(number) && number[index] >= '0' && number[index] <= '9' {
			index++
		}
	} else {
		return false
	}
	if index < len(number) && number[index] == '.' {
		index++
		start := index
		for index < len(number) && number[index] >= '0' && number[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	if index < len(number) && (number[index] == 'e' || number[index] == 'E') {
		index++
		if index < len(number) && (number[index] == '+' || number[index] == '-') {
			index++
		}
		start := index
		for index < len(number) && number[index] >= '0' && number[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	return index == len(number)
}

func (decoder *piJSONDecoder) readNonSpace() (byte, error) {
	for {
		character, err := decoder.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if !isPiJSONSpace(character) {
			return character, nil
		}
	}
}

func (decoder *piJSONDecoder) requireEOF() error {
	for {
		character, err := decoder.reader.ReadByte()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !isPiJSONSpace(character) {
			return fmt.Errorf("Pi event contains trailing values")
		}
	}
}

func isPiJSONSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func (capture *piOutputCapture) consumeLine(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	capture.recordCount++
	if capture.recordCount == 1 {
		prelude, err := decodePiCwdPrelude(line)
		if err != nil {
			capture.boundaryError = true
			return
		}
		capture.prelude = prelude
		return
	}
	if capture.recordCount == 2 {
		header, err := decodePiSessionHeader(line)
		if err != nil || capture.prelude.CWD != header.CWD {
			capture.boundaryError = true
			return
		}
		if capture.session != nil && capture.session.value != "" && capture.session.value != header.ID {
			capture.boundaryError = true
			return
		}
		if capture.session != nil && capture.session.hasHeader && capture.session.header != header {
			capture.boundaryError = true
			return
		}
		capture.header = header
		capture.headerRaw = append([]byte(nil), line...)
		var session Session
		if capture.session != nil {
			session = *capture.session
		} else {
			// An empty cursor denotes the virtual root before a new Session's first entry.
			session.hasCursor = true
		}
		session.value = header.ID
		session.header = header
		session.hasHeader = true
		capture.session = &session
		return
	}
	object, objectErr := decodePiRawObject(line)
	if objectErr == nil {
		if schemaRaw, hasSchema := object["schema"]; hasSchema {
			var schema string
			if json.Unmarshal(schemaRaw, &schema) == nil && schema == piCwdPreludeSchema {
				capture.boundaryError = true
				return
			}
		}
		if typeRaw, hasType := object["type"]; hasType {
			var typeName string
			if json.Unmarshal(typeRaw, &typeName) == nil && typeName == "session" {
				capture.boundaryError = true
				return
			}
		}
	}
	event, err := decodePiEvent(bytes.NewReader(line))
	if err != nil {
		capture.lifecycleMalformed = true
		return
	}
	capture.observeEvent(event)
}

func (capture *piOutputCapture) observeEvent(event piEvent) {
	capture.eventSequence++
	if event.Type == "session" {
		capture.boundaryError = true
		return
	}
	if capture.agentEnded && event.Type != "agent_settled" {
		capture.boundaryError = true
		return
	}
	switch event.Type {
	case "agent_start":
		if capture.agentStarted || capture.agentEnded {
			capture.boundaryError = true
			return
		}
		capture.agentStarted = true
	case "message_end":
		if !capture.agentStarted || capture.agentEnded {
			capture.boundaryError = true
			return
		}
		if event.Message != nil && event.Message.Role == "assistant" {
			capture.messageEnd = lifecycleMessage(event.Message)
			capture.messageEndSequence = capture.eventSequence
			capture.setResponse(event.Message)
		}
	case "turn_end":
		if !capture.agentStarted || capture.agentEnded {
			capture.boundaryError = true
			return
		}
		if event.Message != nil && event.Message.Role == "assistant" {
			capture.turnEnd = lifecycleMessage(event.Message)
			capture.turnEndSequence = capture.eventSequence
			capture.setResponse(event.Message)
		}
	case "agent_end":
		if !capture.agentStarted || capture.agentEnded {
			capture.boundaryError = true
			return
		}
		capture.agentEnded = true
		capture.agentEndSequence = capture.eventSequence
		for index := len(event.Messages) - 1; index >= 0; index-- {
			if event.Messages[index].Role == "assistant" {
				capture.agentEnd = lifecycleMessage(&event.Messages[index])
				capture.setResponse(&event.Messages[index])
				break
			}
		}
	case "agent_settled":
		if !capture.agentEnded {
			capture.lifecycleMalformed = true
		}
	}
}

func lifecycleMessage(message *piMessage) *piLifecycleMessage {
	text, overflowed := boundedPiMessageText(message)
	if overflowed {
		text = ""
	}
	return &piLifecycleMessage{text: text, stopReason: message.StopReason}
}

func (capture *piOutputCapture) setResponse(message *piMessage) {
	capture.responseSeen = true
	text, overflowed := boundedPiMessageText(message)
	capture.response = nil
	capture.responseOverflow = overflowed
	if !overflowed {
		capture.response = []byte(text)
	}
}

func boundedPiMessageText(message *piMessage) (string, bool) {
	if message.overflow {
		return "", true
	}
	var result strings.Builder
	for _, content := range message.Content {
		if content.Type != "text" || content.Text == "" {
			continue
		}
		if result.Len()+len(content.Text) > maximumPiResponseBytes {
			return "", true
		}
		result.WriteString(content.Text)
	}
	return result.String(), false
}

func (capture *piOutputCapture) flush() {
	if capture.longLine != nil {
		capture.finishLine()
		capture.lifecycleMalformed = true
		return
	}
	if capture.droppingLine {
		capture.line = nil
		capture.droppingLine = false
		capture.lifecycleMalformed = true
		return
	}
	if len(capture.line) > 0 {
		capture.lifecycleMalformed = true
		capture.line = nil
	}
}

func (capture *piOutputCapture) Session() *Session {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.flush()
	if capture.session == nil || capture.recordCount < 2 || capture.header.ID == "" {
		return nil
	}
	copy := *capture.session
	return &copy
}

func (capture *piOutputCapture) Evidence() piStdoutEvidence {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.flush()
	result := piStdoutEvidence{
		prelude: capture.prelude, header: capture.header,
		headerRaw: append([]byte(nil), capture.headerRaw...),
	}
	if capture.session != nil && capture.recordCount >= 2 && capture.header.ID != "" {
		copy := *capture.session
		result.session = &copy
	}
	if capture.boundaryError {
		result.state = piEvidenceBoundaryError
		return result
	}
	if capture.recordCount < 2 || !capture.agentStarted || !capture.agentEnded ||
		capture.messageEnd == nil || capture.turnEnd == nil || capture.agentEnd == nil {
		result.state = piEvidenceUnavailable
		return result
	}
	if capture.messageEndSequence >= capture.turnEndSequence || capture.turnEndSequence >= capture.agentEndSequence {
		result.state = piEvidenceBoundaryError
		return result
	}
	if capture.messageEnd.text != capture.turnEnd.text || capture.messageEnd.text != capture.agentEnd.text ||
		capture.messageEnd.stopReason != capture.turnEnd.stopReason ||
		capture.messageEnd.stopReason != capture.agentEnd.stopReason {
		result.state = piEvidenceBoundaryError
		return result
	}
	if capture.messageEnd.stopReason != "stop" || strings.TrimSpace(capture.messageEnd.text) == "" {
		result.state = piEvidenceUnavailable
		return result
	}
	if capture.lifecycleMalformed {
		result.state = piEvidenceUnavailable
		return result
	}
	result.state = piEvidenceComplete
	return result
}

func (capture *piOutputCapture) Result() rundomain.ProcessOutputCaptureResult {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.flush()
	return rundomain.ProcessOutputCaptureResult{
		Stdout:     string(capture.response),
		Overflowed: capture.transportOverflowed && !capture.responseSeen && !capture.responseOverflow,
	}
}

var _ rundomain.ProcessOutputCapture = (*piOutputCapture)(nil)
