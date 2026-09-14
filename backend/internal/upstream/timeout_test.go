package upstream

import (
	"testing"
	"time"
)

func TestWithTimeoutDoesNotChangeManagementClient(t *testing.T) {
	original := NewClient()
	probe := original.WithTimeout(30 * time.Second)
	if original.http.Timeout != 10*time.Second || probe.http.Timeout != 30*time.Second || original.http == probe.http {
		t.Fatal("timeout isolation failed")
	}
}
