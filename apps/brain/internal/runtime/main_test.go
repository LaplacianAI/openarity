package runtime

import (
	"os"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

// TestMain drops the migrated template the database tests copied.
func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
