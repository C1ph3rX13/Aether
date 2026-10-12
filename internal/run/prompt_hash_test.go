package run

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

func ExplorePromptTemplateHash(prompt string, fallbackPrompt string) string {
	return ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, "", "")
}

func ExplorePromptTemplateHashWithRecovery(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt string) string {
	material := "aether.explore-prompts/test-v1\n" + strconv.Itoa(len(prompt)) + "\n" + prompt +
		strconv.Itoa(len(fallbackPrompt)) + "\n" + fallbackPrompt +
		strconv.Itoa(len(recoveryPrompt)) + "\n" + recoveryPrompt +
		strconv.Itoa(len(recoveryConcludePrompt)) + "\n" + recoveryConcludePrompt
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func BootstrapPromptTemplateHash(prompt string, fallbackPrompt string) string {
	return BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, "", "")
}

func BootstrapPromptTemplateHashWithRecoveryModes(prompt, fallbackPrompt, recoveryPrompt, recoveryConcludePrompt string) string {
	material := "aether.bootstrap-prompts/test-v1\n" + strconv.Itoa(len(prompt)) + "\n" + prompt +
		strconv.Itoa(len(fallbackPrompt)) + "\n" + fallbackPrompt +
		strconv.Itoa(len(recoveryPrompt)) + "\n" + recoveryPrompt +
		strconv.Itoa(len(recoveryConcludePrompt)) + "\n" + recoveryConcludePrompt
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}
