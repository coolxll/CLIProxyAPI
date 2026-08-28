package helps

import (
	"testing"
	"time"
)

func TestOneShotTTLSetConsumesOnce(t *testing.T) {
	set := NewOneShotTTLSet(2)
	if !set.Mark("request", time.Minute) {
		t.Fatal("Mark returned false")
	}
	if !set.Consume("request") {
		t.Fatal("first Consume returned false")
	}
	if set.Consume("request") {
		t.Fatal("second Consume returned true")
	}
}

func TestOneShotTTLSetExpiresAndEvicts(t *testing.T) {
	set := NewOneShotTTLSet(1)
	set.Mark("expired", time.Nanosecond)
	time.Sleep(time.Millisecond)
	if set.Consume("expired") {
		t.Fatal("expired key was consumed")
	}

	set.Mark("first", time.Minute)
	set.Mark("second", time.Hour)
	if set.Consume("first") {
		t.Fatal("oldest key was not evicted")
	}
	if !set.Consume("second") {
		t.Fatal("newest key was evicted")
	}
}
