package secrets

// ToolName is the program that reads this platform's keychain.
const ToolName = "security"

// notFoundExit is errSecItemNotFound as the tool reports it.
const notFoundExit = "44"

func toolArgs(service, account string) []string {
	return []string{"find-generic-password", "-s", service, "-a", account, "-w"}
}
