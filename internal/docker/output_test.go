package docker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/api/pkg/stdcopy"
)

func TestProcessOutputCaptureReceivesAllStdoutWhileStderrStaysBounded(t *testing.T) {
	capture := &recordingOutputCapture{result: "final"}
	limits := OutputLimits{StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes}
	output := newProcessOutputWithCapture(limits, capture)
	var stream bytes.Buffer
	stdout := strings.Repeat("noise", int(MinOutputLimitBytes/5)*3) + "final"
	writeOutputFrame(t, &stream, stdcopy.Stdout, []byte(stdout))
	if err := output.drain(&stream); err != nil {
		t.Fatal(err)
	}
	retainedStdout, retainedStderr, overflowed := output.result()
	if retainedStdout != "final" || retainedStderr != "" || overflowed {
		t.Fatalf("output=%q stderr=%q overflowed=%t", retainedStdout, retainedStderr, overflowed)
	}
	if capture.stdoutBytes != len(stdout) {
		t.Fatalf("capture received %d stdout bytes, want %d", capture.stdoutBytes, len(stdout))
	}
}

type recordingOutputCapture struct {
	stdoutBytes int
	result      string
}

func (capture *recordingOutputCapture) WriteStdout(data []byte) { capture.stdoutBytes += len(data) }
func (capture *recordingOutputCapture) WriteStderr([]byte)      {}
func (capture *recordingOutputCapture) Result() rundomain.ProcessOutputCaptureResult {
	return rundomain.ProcessOutputCaptureResult{Stdout: capture.result}
}

func writeOutputFrame(t *testing.T, stream *bytes.Buffer, streamType stdcopy.StdType, data []byte) {
	t.Helper()
	header := make([]byte, dockerStreamHeaderBytes)
	header[0] = byte(streamType)
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	_, _ = stream.Write(header)
	_, _ = stream.Write(data)
}

func TestProcessOutputRejectsTruncatedDockerFrame(t *testing.T) {
	var stream bytes.Buffer
	header := make([]byte, dockerStreamHeaderBytes)
	header[0] = byte(stdcopy.Stdout)
	binary.BigEndian.PutUint32(header[4:], 32)
	stream.Write(header)
	stream.WriteString("partial")

	output := newProcessOutput(OutputLimits{
		StdoutBytes: MinOutputLimitBytes, StderrBytes: MinOutputLimitBytes,
	})
	err := output.drain(&stream)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame error = %v, want unexpected EOF", err)
	}
	stdout, _, overflowed := output.result()
	if stdout != "partial" || overflowed {
		t.Fatalf("truncated frame output=%q overflowed=%t", stdout, overflowed)
	}
	if code := processOutputError(err, false, overflowed); code != rundomain.ProcessErrorStreamRead {
		t.Fatalf("truncated frame code=%s", code)
	}
}
