package triggers

import (
	"os"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
