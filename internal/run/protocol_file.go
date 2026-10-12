package run

import (
	"path"
	"strings"

	"github.com/google/uuid"
)

// ProtocolFile is an Adapter-owned submission schema prepared before Exec.
// Its target belongs to one Invocation, independently of the input snapshot.
type ProtocolFile struct {
	InvocationID string
	// Filename is a single safe file name beneath the Invocation directory.
	Filename string
	// Writable allows the agent to fill the file before process completion.
	Writable bool
	Bytes    []byte
}

func (file ProtocolFile) Path() string {
	return "/tmp/aether-invocations/" + file.InvocationID + "/" + file.Filename
}

func (file ProtocolFile) Validate() error {
	id, err := uuid.Parse(file.InvocationID)
	if err != nil || id.String() != file.InvocationID || id == uuid.Nil ||
		len(file.Bytes) == 0 || len(file.Bytes) > MaximumResultBytes {
		return newError(ErrorInvalidArgument, "Invocation protocol file is invalid")
	}
	name := file.Filename
	if strings.IndexByte(name, 0) >= 0 || path.Base(name) != name || path.Clean(name) != name ||
		name == "." || name == ".." || strings.TrimSpace(name) == "" {
		return newError(ErrorInvalidArgument, "Invocation protocol file name is invalid")
	}
	return nil
}
