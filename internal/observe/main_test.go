package observe

import (
	"os"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
