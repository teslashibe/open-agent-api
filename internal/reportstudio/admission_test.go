package reportstudio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmissionLevelsAndSaturation(t *testing.T) {
	for _, level := range []int{1, 2, 4, 8} {
		t.Run(string(rune('0'+level)), func(t *testing.T) {
			admission := NewAdmission(level, 0)
			releases := make([]func(), 0, level)
			for range level {
				release, err := admission.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				releases = append(releases, release)
			}
			if admission.Active() != level {
				t.Fatalf("active = %d, want %d", admission.Active(), level)
			}
			if _, err := admission.Acquire(context.Background()); !errors.Is(err, ErrAdmissionFull) {
				t.Fatalf("saturation error = %v", err)
			}
			for _, release := range releases {
				release()
			}
		})
	}
}

func TestAdmissionQueuesWithinBoundAndReleases(t *testing.T) {
	admission := NewAdmission(1, 1)
	releaseFirst, err := admission.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		release, err := admission.Acquire(context.Background())
		if err == nil {
			acquired <- release
		}
	}()
	deadline := time.After(time.Second)
	for admission.Waiting() != 1 {
		select {
		case <-deadline:
			t.Fatal("request did not enter bounded queue")
		default:
		}
	}
	releaseFirst()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("queued request was not admitted")
	}
}

func TestAdmissionRejectsExpiredContextWithoutConsumingCapacity(t *testing.T) {
	admission := NewAdmission(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := admission.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v", err)
	}
	if admission.Active() != 0 || admission.Waiting() != 0 {
		t.Fatalf("admission state = active %d waiting %d", admission.Active(), admission.Waiting())
	}
}
