package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

const (
	maximumRequestBodyBytes int64 = rundomain.MaximumBlackboardInputBytes + 1024*1024

	runControlSmallBodyBytes      int64 = 64 * 1024
	runControlSchedulingBodyBytes       = 1024 * 1024
	runControlFinishBodyBytes           = 2*rundomain.MaximumResultBytes + runControlSmallBodyBytes
)

func bindJSON(c *gin.Context, destination any, maximumBytes int64) bool {
	_, err := decodeJSONRequest(c, destination, maximumBytes)
	if err != nil {
		writeRequestError(c, err)
		return false
	}
	return true
}

// bindProjectJSON keeps required Revision presence in the HTTP adapter;
// domain commands use an integer and permit Revision zero.
func bindProjectJSON(c *gin.Context, destination any) bool {
	body, err := decodeJSONRequest(c, destination, maximumRequestBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeRequestError(c, err)
		return false
	}
	// Struct decoding folds case; reject aliases so the presence check and
	// the command agree on which field supplies the Revision.
	for field := range envelope {
		if field != "expected_revision" && strings.EqualFold(field, "expected_revision") {
			writeRequestError(c, errors.New("expected_revision must use its exact field name"))
			return false
		}
	}
	revision, present := envelope["expected_revision"]
	if !present {
		writeRequestError(c, errors.New("expected_revision is required"))
		return false
	}
	if bytes.Equal(bytes.TrimSpace(revision), []byte("null")) {
		writeRequestError(c, errors.New("expected_revision must be an integer"))
		return false
	}
	return true
}

// Read the entire bounded body before decoding so trailing data cannot evade
// the limit or allow a command to run on a partially validated request.
func decodeJSONRequest(c *gin.Context, destination any, maximumBytes int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maximumBytes))
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, errors.New("request must contain exactly one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("request must contain exactly one JSON object")
	}
	if err := binding.Validator.ValidateStruct(destination); err != nil {
		return nil, err
	}
	return body, nil
}

func writeRequestError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	var maximumError *http.MaxBytesError
	if errors.As(err, &maximumError) {
		status = http.StatusRequestEntityTooLarge
	}
	writeCodedError(c, status, rundomain.ErrorInvalidArgument, err.Error())
}
