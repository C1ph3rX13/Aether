package docker

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/api/pkg/stdcopy"
)

const (
	DefaultOutputLimitBytes int64 = 1 << 20
	MinOutputLimitBytes     int64 = 64 << 10
	MaxOutputLimitBytes     int64 = 8 << 20
	dockerStreamHeaderBytes       = 8
)

// OutputLimits bounds retained process output independently per stream.
type OutputLimits struct {
	StdoutBytes int64
	StderrBytes int64
}

func DefaultOutputLimits() OutputLimits {
	return OutputLimits{
		StdoutBytes: DefaultOutputLimitBytes,
		StderrBytes: DefaultOutputLimitBytes,
	}
}

func (limits OutputLimits) Validate() error {
	if err := validateOutputLimit("stdout", limits.StdoutBytes); err != nil {
		return err
	}
	return validateOutputLimit("stderr", limits.StderrBytes)
}

func validateOutputLimit(name string, value int64) error {
	if value < MinOutputLimitBytes || value > MaxOutputLimitBytes {
		return fmt.Errorf(
			"%s output limit must be between %d and %d bytes, got %d",
			name, MinOutputLimitBytes, MaxOutputLimitBytes, value,
		)
	}
	return nil
}

type processOutput struct {
	stdout  *boundedCollector
	stderr  *boundedCollector
	capture rundomain.ProcessOutputCapture
}

func newProcessOutput(limits OutputLimits) *processOutput {
	return newProcessOutputWithCapture(limits, nil)
}

func newProcessOutputWithCapture(
	limits OutputLimits,
	capture rundomain.ProcessOutputCapture,
) *processOutput {
	return &processOutput{
		stdout:  newBoundedCollector(limits.StdoutBytes),
		stderr:  newBoundedCollector(limits.StderrBytes),
		capture: capture,
	}
}

func (output *processOutput) drain(reader io.Reader) error {
	var header [dockerStreamHeaderBytes]byte
	for {
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read Docker stream header: %w", err)
		}
		frameBytes := int64(binary.BigEndian.Uint32(header[4:]))
		var destination io.Writer
		streamType := stdcopy.StdType(header[0])
		switch streamType {
		case stdcopy.Stdin, stdcopy.Stdout:
			destination = output.stdout
			if output.capture != nil {
				destination = captureWriter{capture: output.capture, stream: streamStdout}
			}
		case stdcopy.Stderr:
			destination = output.stderr
			if output.capture != nil {
				destination = captureWriter{
					capture: output.capture, stream: streamStderr, collector: output.stderr,
				}
			}
		case stdcopy.Systemerr:
			if err := copyDockerFrame(io.Discard, reader, frameBytes); err != nil {
				return fmt.Errorf("read Docker system error frame: %w", err)
			}
			return fmt.Errorf("Docker daemon reported an attach stream error")
		default:
			return fmt.Errorf("unrecognized Docker stream header: %d", header[0])
		}
		if err := copyDockerFrame(destination, reader, frameBytes); err != nil {
			return fmt.Errorf("read Docker stream frame: %w", err)
		}
	}
}

func copyDockerFrame(destination io.Writer, reader io.Reader, frameBytes int64) error {
	copied, err := io.CopyN(destination, reader, frameBytes)
	if err == io.EOF && copied != frameBytes {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (output *processOutput) result() (stdout, stderr string, overflowed bool) {
	stdout, stdoutOverflowed := output.stdout.result()
	if output.capture != nil {
		captured := output.capture.Result()
		stdout = captured.Stdout
		stdoutOverflowed = captured.Overflowed
	}
	stderr, stderrOverflowed := output.stderr.result()
	return stdout, stderr, stdoutOverflowed || stderrOverflowed
}

type outputStream uint8

const (
	streamStdout outputStream = iota
	streamStderr
)

type captureWriter struct {
	capture   rundomain.ProcessOutputCapture
	stream    outputStream
	collector io.Writer
}

func (writer captureWriter) Write(data []byte) (int, error) {
	if writer.collector != nil {
		if _, err := writer.collector.Write(data); err != nil {
			return 0, err
		}
	}
	if writer.stream == streamStdout {
		writer.capture.WriteStdout(data)
	} else {
		writer.capture.WriteStderr(data)
	}
	return len(data), nil
}

// boundedCollector retains a prefix while accepting all bytes so Docker's
// attach stream continues draining after the retention limit is reached.
type boundedCollector struct {
	mu       sync.Mutex
	limit    int
	data     bytes.Buffer
	overflow bool
}

func newBoundedCollector(limit int64) *boundedCollector {
	return &boundedCollector{limit: int(limit)}
}

func (collector *boundedCollector) Write(data []byte) (int, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()

	remaining := collector.limit - collector.data.Len()
	if remaining > 0 {
		retained := len(data)
		if retained > remaining {
			retained = remaining
			collector.overflow = true
		}
		_, _ = collector.data.Write(data[:retained])
	} else if len(data) > 0 {
		collector.overflow = true
	}
	return len(data), nil
}

func (collector *boundedCollector) result() (string, bool) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.data.String(), collector.overflow
}

func processOutputError(streamErr error, closeRequested, overflowed bool) rundomain.ProcessErrorCode {
	if overflowed {
		return rundomain.ProcessErrorOutputTooLarge
	}
	if streamErr != nil && !closeRequested {
		return rundomain.ProcessErrorStreamRead
	}
	return ""
}
