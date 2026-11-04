package session

// MemoryDestination contains public connection metadata only, never credentials.
// The daemon constructs it from the exact immutable provider configuration.
type MemoryDestination struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
}
