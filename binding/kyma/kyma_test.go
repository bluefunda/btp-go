package kyma

import (
	"errors"
	"testing"

	"github.com/bluefunda/btp-go/binding"
)

func TestKymaProvider_AllMethodsReturnErrUnsupported(t *testing.T) {
	p := NewProvider()

	_, err := p.Connectivity("")
	if !errors.Is(err, binding.ErrUnsupported) {
		t.Errorf("Connectivity: expected ErrUnsupported, got %v", err)
	}

	_, err = p.Destination("")
	if !errors.Is(err, binding.ErrUnsupported) {
		t.Errorf("Destination: expected ErrUnsupported, got %v", err)
	}

	_, err = p.XSUAA("")
	if !errors.Is(err, binding.ErrUnsupported) {
		t.Errorf("XSUAA: expected ErrUnsupported, got %v", err)
	}
}
