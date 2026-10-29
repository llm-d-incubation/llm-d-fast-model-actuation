package launcherpopulator

import (
	"testing"

	_ "k8s.io/component-base/logs/testinit"
)

func TestMain(m *testing.M) {
	m.Run()
}
