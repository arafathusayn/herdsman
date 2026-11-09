package secrets

// ToolName is the program that reads this platform's keyring.
const ToolName = "secret-tool"

// notFoundExit is what the tool returns for an item that is not stored.
const notFoundExit = "1"

func toolArgs(service, account string) []string {
	return []string{"lookup", "service", service, "account", account}
}
