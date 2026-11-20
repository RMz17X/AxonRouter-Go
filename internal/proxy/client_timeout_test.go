package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestClientWithResponseHeaderTimeout(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 2 * time.Minute
	original := &http.Client{Transport: NewFallbackTransport(base)}

	clone := ClientWithResponseHeaderTimeout(original, 10*time.Minute)
	if clone == original {
		t.Fatal("client was mutated instead of copied")
	}
	wrapper, ok := clone.Transport.(*FallbackTransport)
	if !ok {
		t.Fatalf("wrapper type = %T", clone.Transport)
	}
	inner, ok := wrapper.Base.(*http.Transport)
	if !ok {
		t.Fatalf("base type = %T", wrapper.Base)
	}
	if inner.ResponseHeaderTimeout != 10*time.Minute {
		t.Errorf("clone timeout = %s", inner.ResponseHeaderTimeout)
	}
	originalInner := original.Transport.(*FallbackTransport).Base.(*http.Transport)
	if originalInner.ResponseHeaderTimeout != 2*time.Minute {
		t.Errorf("original transport was mutated: %s", originalInner.ResponseHeaderTimeout)
	}
}

func TestClientWithResponseHeaderTimeoutKeepsStrictMarker(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	strict := ForbidDirectReplay(&http.Client{Transport: base})
	clone := ClientWithResponseHeaderTimeout(strict, 10*time.Minute)
	if !ForbidsDirectReplay(clone) {
		t.Error("strict replay marker was lost")
	}
	inner := clone.Transport.(forbidDirectReplayTransport).base.(*http.Transport)
	if inner.ResponseHeaderTimeout != 10*time.Minute {
		t.Errorf("inner timeout = %s", inner.ResponseHeaderTimeout)
	}
}
