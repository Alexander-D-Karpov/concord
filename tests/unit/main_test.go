package auth_test

import (
	"os"
	"testing"

	"github.com/Alexander-D-Karpov/concord/tests/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Teardown()
	os.Exit(code)
}
