package worker

import (
	"encoding/json"
	"fmt"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func captureErrorResult(
	session *Session,
	diagnostics Diagnostics,
	code CaptureErrorCode,
	detail string,
) (CaptureResult, error) {
	return CaptureResult{
		Session: session, Diagnostics: diagnostics,
		CaptureFailureReason: captureFailureReason(code),
	}, &CaptureError{Code: code, Detail: detail}
}

func asCaptureError(err error, target **CaptureError) bool {
	if captureErr, ok := err.(*CaptureError); ok {
		*target = captureErr
		return true
	}
	return false
}

func captureFailureReason(code CaptureErrorCode) rundomain.CaptureFailureReason {
	switch code {
	case CaptureTimedOut:
		return rundomain.CaptureFailureTimedOut
	case CaptureNoCandidate:
		return rundomain.CaptureFailureNoCandidate
	case CaptureMultipleCandidates:
		return rundomain.CaptureFailureMultipleCandidates
	case CaptureInvalidCandidate:
		return rundomain.CaptureFailureInvalidCandidate
	case CaptureCandidateTooLarge:
		return rundomain.CaptureFailureCandidateTooLarge
	case CaptureBoundaryError:
		return rundomain.CaptureFailureBoundaryError
	default:
		return ""
	}
}

func diagnosticSummary(value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("[redacted agent output: %d bytes]", len(value))
}

type jsonContainerFrame struct {
	kind byte
	keys map[string]struct{}
}

func hasDuplicateObjectKeys(candidate []byte) bool {
	frames := make([]jsonContainerFrame, 0, 4)
	for index := 0; index < len(candidate); index++ {
		if candidate[index] == '"' {
			end, ok := scanJSONString(candidate, index)
			if !ok {
				break
			}
			next := end
			for next < len(candidate) && isJSONWhitespace(candidate[next]) {
				next++
			}
			if next < len(candidate) && candidate[next] == ':' && len(frames) > 0 &&
				frames[len(frames)-1].kind == '{' {
				var key string
				if err := json.Unmarshal(candidate[index:end], &key); err == nil {
					frame := &frames[len(frames)-1]
					if _, exists := frame.keys[key]; exists {
						return true
					}
					frame.keys[key] = struct{}{}
				}
			}
			index = end - 1
			continue
		}
		switch candidate[index] {
		case '{':
			frames = append(frames, jsonContainerFrame{kind: '{', keys: make(map[string]struct{})})
		case '[':
			frames = append(frames, jsonContainerFrame{kind: '['})
		case '}', ']':
			if len(frames) > 0 {
				frames = frames[:len(frames)-1]
			}
		}
	}
	return false
}

func hasMultipleTopLevelValues(candidate []byte) bool {
	stack := make([]byte, 0, 4)
	values := 0
	for index := 0; index < len(candidate); index++ {
		if candidate[index] == '"' {
			if len(stack) == 0 {
				values++
			}
			end, ok := scanJSONString(candidate, index)
			if !ok {
				break
			}
			index = end - 1
			continue
		}
		if len(stack) == 0 && !isJSONWhitespace(candidate[index]) && candidate[index] != ',' {
			if candidate[index] != '{' && candidate[index] != '[' && candidate[index] != '}' && candidate[index] != ']' {
				values++
				for index+1 < len(candidate) && !isJSONWhitespace(candidate[index+1]) &&
					!strings.ContainsRune(",{}[]", rune(candidate[index+1])) {
					index++
				}
				continue
			}
		}
		switch candidate[index] {
		case '{', '[':
			if len(stack) == 0 {
				values++
			}
			stack = append(stack, candidate[index])
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return values > 1
}

func scanJSONString(candidate []byte, start int) (int, bool) {
	for index := start + 1; index < len(candidate); index++ {
		if candidate[index] == '\\' {
			index++
			continue
		}
		if candidate[index] == '"' {
			return index + 1, true
		}
	}
	return len(candidate), false
}

func isJSONWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func isSingleJSONObject(candidate []byte) bool {
	trimmed := strings.TrimSpace(string(candidate))
	return json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") &&
		strings.HasSuffix(trimmed, "}") && !hasMultipleTopLevelValues([]byte(trimmed))
}
