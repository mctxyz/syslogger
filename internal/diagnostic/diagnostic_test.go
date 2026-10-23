package diagnostic

import (
	"testing"
	"time"
)

func TestRegistrationFactsAndRecovery(t *testing.T) {
	for _, kind := range []string{RegisterNameInvalid, RegisterDuplicate, RegisterReserved} {
		at := time.Unix(100, 0)
		tracker := New(at)
		initial := tracker.Current
		tracker.Registered(at.Add(time.Second))
		if tracker.Current != initial {
			t.Fatal("普通成功注册改变初始化事实")
		}
		tracker.RegistrationFailed(kind, at.Add(2*time.Second))
		failed := tracker.Current
		tracker.RegistrationFailed(RegisterReserved, at)
		if tracker.Current != failed || failed.State != 2 {
			t.Fatal("更早诊断改变当前记录")
		}
		tracker.Registered(at.Add(time.Second))
		if tracker.Current != failed {
			t.Fatal("更早成功错误地恢复了当前诊断")
		}
		tracker.Registered(at.Add(3 * time.Second))
		if tracker.Current.State != 1 || tracker.kind != "" {
			t.Fatal("成功注册未恢复当前问题")
		}
	}
}

func TestConfigurationFailurePersists(t *testing.T) {
	at := time.Unix(100, 0)
	tracker := New(at)
	initial := tracker.Current
	tracker.ConfigurationFailed(at.Add(-time.Second))
	if tracker.Current != initial || tracker.configurationFailed {
		t.Fatal("更早的配置诊断覆盖了当前事实")
	}
	tracker.ConfigurationFailed(at.Add(time.Second))
	failed := tracker.Current
	if failed.State != 3 || !tracker.configurationFailed {
		t.Fatal("配置异常未形成持续不健康事实")
	}
	tracker.Registered(at.Add(2 * time.Second))
	tracker.RegistrationFailed(RegisterDuplicate, at.Add(3*time.Second))
	tracker.Registered(at.Add(4 * time.Second))
	if tracker.Current != failed {
		t.Fatal("注册事件覆盖了配置初始化异常")
	}
}
