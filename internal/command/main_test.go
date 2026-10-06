package command

import (
	"os"
	"testing"
)

// TestMain isolates tests from the developer's user-wide rag-go configuration.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rag-go-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("RAG_GO_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
