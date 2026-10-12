package worker

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// piResultEvidenceModule is the single Worker-owned authority for Pi Invocation
// evidence. Its interface hides stdout lifecycle state, Session format,
// cursor verification, stopped-file access, and TOML candidate normalization
// from generic Adapter and Run code.
type piResultEvidenceModule struct{}

func newPiResultEvidence() resultEvidenceModule { return &piResultEvidenceModule{} }

func (e *piResultEvidenceModule) NewOutputCapture(session *Session) streamingCapture {
	return newPiOutputCapture(session)
}

func piSessionDirFromArgv(argv []string) string {
	var sessionDir string
	for index, argument := range argv {
		if argument != "--session-dir" {
			continue
		}
		if sessionDir != "" || index+1 >= len(argv) {
			return ""
		}
		candidate := argv[index+1]
		if !canonicalPiAbsolutePath(candidate) {
			return ""
		}
		sessionDir = candidate
	}
	return sessionDir
}

func piSessionFilePath(sessionDir string, header piSessionHeader) string {
	if !canonicalPiAbsolutePath(sessionDir) {
		return ""
	}
	fileTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(header.Timestamp)
	return path.Join(sessionDir, fileTimestamp+"_"+header.ID+".jsonl")
}

func (e *piResultEvidenceModule) SessionDirectory(argv []string) string {
	return piSessionDirFromArgv(argv)
}

func (e *piResultEvidenceModule) CanContinueSession(session *Session) bool {
	return session != nil && session.hasHeader && session.hasCursor && session.cursor != "" &&
		session.header.ID == session.value && canonicalPiEntryID(session.cursor)
}

func (e *piResultEvidenceModule) Capture(
	ctx context.Context,
	invocation Invocation,
	output ProcessOutput,
) (CaptureResult, error) {
	diagnostics := Diagnostics{StdoutSummary: diagnosticSummary(output.Stdout), StderrSummary: diagnosticSummary(output.Stderr)}
	capture, ok := invocation.capture.(*piOutputCapture)
	if !ok {
		capture = newPiOutputCapture(invocation.session)
		capture.WriteStdout([]byte(output.Stdout))
	}
	stdout := capture.Evidence()
	if stdout.state == piEvidenceBoundaryError {
		return captureErrorResult(nil, diagnostics, CaptureBoundaryError, "Pi stdout boundary is contradictory")
	}
	if output.ErrorCode != "" {
		return captureErrorResult(nil, diagnostics, CaptureProcessFailed, "agent process did not exit successfully before result capture")
	}
	if output.Cancelled || output.TimedOut {
		code := CaptureCancelled
		if output.TimedOut {
			code = CaptureTimedOut
		}
		return captureErrorResult(nil, diagnostics, code, "process did not produce a usable result")
	}
	if !output.Exited || output.ExitCode != 0 {
		return captureErrorResult(nil, diagnostics, CaptureProcessFailed, "agent process did not exit successfully before result capture")
	}
	if invocation.protocolFile == nil || invocation.protocolFile.Filename != piResultFilename {
		return captureErrorResult(nil, diagnostics, CaptureBoundaryError, "Pi result file is unavailable")
	}
	sessionEvidence, resultFile := readPiInvocationEvidence(ctx, invocation, output, stdout, capture)
	session := sessionEvidence.session
	switch sessionEvidence.state {
	case piEvidenceBoundaryError:
		return captureErrorResult(nil, diagnostics, CaptureBoundaryError, "Pi Session boundary is invalid")
	case piEvidenceIncomplete:
		if stdout.state == piEvidenceComplete {
			return captureErrorResult(nil, diagnostics, CaptureBoundaryError, "Pi Session and stdout terminal states disagree")
		}
		return captureErrorResult(session, diagnostics, CaptureNoCandidate, "Pi Session Invocation is structurally verified but incomplete")
	}
	if sessionEvidence.state != piEvidenceComplete && stdout.state != piEvidenceComplete {
		return captureErrorResult(nil, diagnostics, CaptureNoCandidate, "Pi result evidence is incomplete")
	}
	if resultFile.Err != nil {
		captureResult, _ := captureErrorResult(session, diagnostics, CaptureNoCandidate, "Pi result file is unavailable")
		captureResult.CaptureDiagnostic = rundomain.CaptureDiagnosticResultFileMissing
		return captureResult, &CaptureError{Code: CaptureNoCandidate, Detail: "Pi result file is unavailable"}
	}
	candidate, hash, code, err := normalizePiTOML(resultFile.Bytes, invocation.resultContract)
	if err != nil {
		captured := CaptureResult{Session: session, Diagnostics: diagnostics, CaptureFailureReason: captureFailureReason(code), CaptureDiagnostic: rundomain.CaptureDiagnosticResultFileInvalid}
		if code == CaptureCandidateTooLarge {
			captured.CaptureDiagnostic = rundomain.CaptureDiagnosticResultFileTooLarge
		}
		return captured, err
	}
	return CaptureResult{
		Candidate: candidate, ResultHash: hash, Session: session, Diagnostics: diagnostics,
		ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
	}, nil
}

func readPiInvocationEvidence(
	ctx context.Context,
	invocation Invocation,
	output ProcessOutput,
	stdout piStdoutEvidence,
	capture *piOutputCapture,
) (piResultEvidence, rundomain.StoppedProcessFile) {
	if capture != nil {
		capture.mu.Lock()
		if capture.fileEvidenceRead {
			session, result := capture.fileSession, capture.fileResult
			capture.mu.Unlock()
			return session, result
		}
		capture.mu.Unlock()
	}
	cache := func(session piResultEvidence, result rundomain.StoppedProcessFile) (piResultEvidence, rundomain.StoppedProcessFile) {
		if capture != nil {
			capture.mu.Lock()
			capture.fileEvidenceRead = true
			capture.fileSession = session
			capture.fileResult = result
			capture.mu.Unlock()
		}
		return session, result
	}
	if stdout.session == nil || stdout.header.ID == "" || invocation.sessionDir == "" ||
		invocation.protocolFile == nil || invocation.protocolFile.Filename != piResultFilename ||
		output.StoppedProcessFileReader == nil {
		return cache(piResultEvidence{state: piEvidenceUnavailable, session: nil}, rundomain.StoppedProcessFile{Err: errors.New("stopped file reader unavailable")})
	}
	if stdout.prelude.CWD != stdout.header.CWD {
		return cache(piResultEvidence{state: piEvidenceBoundaryError, session: stdout.session}, rundomain.StoppedProcessFile{})
	}
	sessionPath := piSessionFilePath(invocation.sessionDir, stdout.header)
	resultPath := invocation.protocolFile.Path()
	if sessionPath == "" || resultPath == "" {
		return cache(piResultEvidence{state: piEvidenceUnavailable, session: nil}, rundomain.StoppedProcessFile{Err: errors.New("stopped file path unavailable")})
	}
	files := output.StoppedProcessFileReader.ReadFiles(ctx, []string{sessionPath, resultPath})
	sessionFile := files[sessionPath]
	resultFile := files[resultPath]
	if sessionFile.Err != nil {
		return cache(piResultEvidence{state: piEvidenceUnavailable, session: nil}, resultFile)
	}
	document, err := parsePiSessionDocument(sessionFile.Bytes)
	if err != nil {
		return cache(piResultEvidence{state: piEvidenceUnavailable, session: nil}, resultFile)
	}
	if !bytes.Equal(document.headerRaw, stdout.headerRaw) || document.header != stdout.header ||
		invocation.session != nil && invocation.session.hasHeader && invocation.session.header != stdout.header {
		return cache(piResultEvidence{state: piEvidenceBoundaryError, session: stdout.session}, resultFile)
	}
	return cache(verifyPiInvocationSuffix(invocation, document, stdout.session), resultFile)
}

func (e *piResultEvidenceModule) ObserveSession(
	ctx context.Context,
	invocation Invocation,
	output ProcessOutput,
) *Session {
	capture, ok := invocation.capture.(*piOutputCapture)
	if !ok && invocation.capture != nil {
		return nil
	}
	if capture == nil {
		capture = newPiOutputCapture(invocation.session)
		capture.WriteStdout([]byte(output.Stdout))
	}
	if output.StoppedProcessFileReader == nil {
		return nil
	}
	stdout := capture.Evidence()
	if stdout.state == piEvidenceBoundaryError || stdout.session == nil {
		return nil
	}
	verified, _ := readPiInvocationEvidence(ctx, invocation, output, stdout, capture)
	if verified.state == piEvidenceComplete || verified.state == piEvidenceIncomplete {
		return verified.session
	}
	return nil
}

var _ resultEvidenceModule = (*piResultEvidenceModule)(nil)
