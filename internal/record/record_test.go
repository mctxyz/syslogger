package record

import (
	"testing"
	"time"
)

func TestApplyCompleteFactByObservedAt(t *testing.T) {
	var current Record
	first := New(3, time.Time{}.Add(-time.Second))
	current.Apply(first)
	if current != first {
		t.Fatal("初始记录拒绝了首条较早时间")
	}
	newer := New(2, time.Unix(100, 100))
	current.Apply(newer)
	current.Apply(New(1, time.Unix(100, 99)))
	if current != newer {
		t.Fatal("旧时间覆盖了完整记录")
	}
	equal := New(1, newer.ObservedAt)
	current.Apply(equal)
	if current != equal || !current.Reported {
		t.Fatal("相同时间未由后提交记录覆盖")
	}
}
